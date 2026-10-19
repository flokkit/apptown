package deployment

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net"
	"strings"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/deployment/internal/deploymentsql"
)

// ProjectDeployment is a deployment listed with the project it belongs to,
// for readers that look across projects: what stands on a target, what the
// host watches after a boot, what the overview lists.
type ProjectDeployment struct {
	Deployment
	Project string `json:"project"` // the project's slug
}

func (s *Deployments) placements(ctx context.Context, in deploymentsql.PlacementsParams) ([]ProjectDeployment, error) {
	rows, err := deploymentsql.New(s.db.SQL()).Placements(ctx, in)
	if err != nil {
		return nil, err
	}
	var out []ProjectDeployment
	for _, r := range rows {
		e, err := deploymentRecord(r.Deployment, nil)
		if err != nil {
			return nil, err
		}
		out = append(out, ProjectDeployment{Deployment: *e, Project: r.Slug})
	}
	return out, nil
}

// OnTarget is every deployment bound to a target, previews included.
func (s *Deployments) OnTarget(ctx context.Context, targetID ids.ID) ([]ProjectDeployment, error) {
	return s.placements(ctx, deploymentsql.PlacementsParams{TargetOnly: true, TargetID: targetID})
}

// All is every deployment across the instance.
func (s *Deployments) All(ctx context.Context) ([]ProjectDeployment, error) {
	return s.placements(ctx, deploymentsql.PlacementsParams{})
}

// Named is every project's deployment of one name.
func (s *Deployments) Named(ctx context.Context, name string) ([]ProjectDeployment, error) {
	return s.placements(ctx, deploymentsql.PlacementsParams{NameOnly: true, Name: name})
}

// StandingWithoutOpenAttempt is the standing deployments no deploy or observation owns.
func (s *Deployments) StandingWithoutOpenAttempt(ctx context.Context) ([]ProjectDeployment, error) {
	return s.placements(ctx, deploymentsql.PlacementsParams{IdleStanding: true, StandingState: Standing.String(), RunningState: AttemptRunning.String(), UncertainState: AttemptUncertain.String()})
}

// Previews is every change's preview row.
func (s *Deployments) Previews(ctx context.Context) ([]*Deployment, error) {
	rows, err := deploymentsql.New(s.db.SQL()).PreviewDeployments(ctx, Preview.String())
	if err != nil {
		return nil, err
	}
	var out []*Deployment
	for _, r := range rows {
		e, err := deploymentRecord(r, nil)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

// RecordAddress records where a deployment is reached.
func (s *Deployments) RecordAddress(ctx context.Context, id ids.ID, url string) error {
	ctx = context.WithoutCancel(ctx) // the deployment answers there: recorded though ctx has ended
	now := s.now()
	_, err := deploymentsql.New(s.db.SQL()).RecordAddress(ctx, deploymentsql.RecordAddressParams{Url: url, UpdatedAt: now, ID: id})
	return err
}

// ErrOwnsAnother: the deployment already records another resource under
// that name.
var ErrOwnsAnother = errors.New("the deployment already records another resource there")

// RecordOwned records, on the deployment's handle, a resource a driver just
// created for it (a bucket), before anything else touches the resource, so
// a failed release still leaves it the deployment's and take-down finds it.
// Recording the same value again is no change; another value is refused.
func (s *Deployments) RecordOwned(ctx context.Context, id ids.ID, key, value string) error {
	now := s.now()
	// Recorded after the resource was made: written though ctx has ended.
	n, err := deploymentsql.New(s.db.SQL()).RecordOwnedResource(context.WithoutCancel(ctx), deploymentsql.RecordOwnedResourceParams{HandleKey: key, HandleValue: value, UpdatedAt: now, ID: id})
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("%w (%s)", ErrOwnsAnother, key)
	}
	return nil
}

// ForgetPreviewContainer drops the preview row a reaped container served.
func (s *Deployments) ForgetPreviewContainer(ctx context.Context, container string) error {
	ctx = context.WithoutCancel(ctx) // the container is reaped: its row goes though ctx has ended
	_, err := deploymentsql.New(s.db.SQL()).ForgetPreviewContainer(ctx, deploymentsql.ForgetPreviewContainerParams{Kind: Preview.String(), Container: container})
	return err
}

// ReserveHostPort records the host port and container of a deployment on
// the built-in host. Only an explicitly bound deployment gets a port; the
// first reservation wins when two deploys race, and the caller reads back
// the port that stands. The candidate is one the kernel just handed out;
// nothing here can hold it, so a boot that finds it taken asks again
// through ResetHostPort.
func (s *Deployments) ReserveHostPort(ctx context.Context, projectID ids.ID, name, container string, candidate int) (int, error) {
	now := s.now()
	r, err := deploymentsql.New(s.db.SQL()).ReserveHostPort(ctx, deploymentsql.ReserveHostPortParams{Candidate: int64(candidate), Container: container, UpdatedAt: now, ProjectID: projectID, Name: name})
	return int(r.HostPort), err
}

// StablePort is the deployment's host port on a Docker host, picked and
// recorded before its first start when it has none. Every standing
// deployment is published at this one port, so Docker keeps it at every
// start of the container and the port the forge probes and shows is the
// one the app answers at (soak finding 58: a port Docker picks is picked
// again at each start). Two boots racing for an unallocated port both pick
// a free one; the first reservation wins and the other reads it. Nothing
// here can hold an OS port: if Docker then finds it taken, the boot fails
// with that reason and ForgetTakenPort gives the next boot a fresh one.
func (s *Deployments) StablePort(ctx context.Context, projectID ids.ID, name, container string) (int, error) {
	e, err := s.ByName(ctx, projectID, name)
	if err != nil || e.TargetID.IsZero() {
		return 0, fmt.Errorf("reserving a port requires an explicitly bound deployment: %w", err)
	}
	if e.HostPort > 0 {
		return e.HostPort, nil
	}
	candidate, err := freePort()
	if err != nil {
		return 0, err
	}
	return s.ReserveHostPort(ctx, projectID, name, container, candidate)
}

// ForgetTakenPort: when a boot failed because the deployment's port was in
// use by something else, record a fresh one so the next boot does not fail
// the same way.
func (s *Deployments) ForgetTakenPort(ctx context.Context, projectID ids.ID, name string, bootErr error) {
	if bootErr == nil || !strings.Contains(bootErr.Error(), "address already in use") {
		return
	}
	fresh, err := freePort()
	if err != nil {
		return
	}
	if err := s.ResetHostPort(ctx, projectID, name, fresh); err != nil {
		log.Printf("deployment %s: re-reserving its port after a bind failure: %v", name, err)
	}
}

// freePort is a port the kernel just handed out on this machine.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// ResetHostPort forgets a reservation whose port turned out to be in use.
func (s *Deployments) ResetHostPort(ctx context.Context, projectID ids.ID, name string, fresh int) error {
	now := s.now()
	_, err := deploymentsql.New(s.db.SQL()).ResetHostPort(ctx, deploymentsql.ResetHostPortParams{HostPort: int64(fresh), UpdatedAt: now, ProjectID: projectID, Name: name})
	return err
}

// ProviderDeployment is a standing deployment on a target an integration
// reaches.
type ProviderDeployment struct {
	Project string `json:"project"` // the project's slug
	Name    string `json:"name"`
	Target  string `json:"target"`
}

// StandingByProvider is, for each provider (an integration's name), the
// standing deployments on the targets it reaches. The built-in host has no
// provider; previews come and go with their changes and are left out.
func (s *Service) StandingByProvider(ctx context.Context) (map[string][]ProviderDeployment, error) {
	targets, err := s.Targets.All(ctx)
	if err != nil {
		return nil, err
	}
	byID := map[ids.ID]*Target{}
	for _, t := range targets {
		if t.Provider != "" {
			byID[t.ID] = t
		}
	}
	all, err := s.Deployments.All(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string][]ProviderDeployment{}
	for _, d := range all {
		t := byID[d.TargetID]
		if t == nil || d.Kind != Standing {
			continue
		}
		out[t.Provider] = append(out[t.Provider], ProviderDeployment{Project: d.Project, Name: d.Name, Target: t.Name})
	}
	return out, nil
}

// DataClassOf is what a standing deployment declares it holds. One that
// cannot be read is of unknown class, "", which protects its data: only a
// declared "synthetic" lets a standing service be replaced when its
// parameters move.
func (s *Deployments) DataClassOf(ctx context.Context, projectID ids.ID, name string) string {
	if projectID.IsZero() || name == "" {
		return ""
	}
	e, err := s.ByName(ctx, projectID, name)
	if err != nil {
		return ""
	}
	return e.DataClass
}

// ProductionOnTargetKind are the standing deployments holding production
// data on targets of a kind.
func (s *Deployments) ProductionOnTargetKind(ctx context.Context, kind string) ([]*Deployment, error) {
	rows, err := deploymentsql.New(s.db.SQL()).ProductionOnTargetKind(ctx, deploymentsql.ProductionOnTargetKindParams{StandingKind: Standing.String(), TargetKind: kind})
	if err != nil {
		return nil, err
	}
	out := make([]*Deployment, 0, len(rows))
	for _, r := range rows {
		dep, err := deploymentRecord(r, nil)
		if err != nil {
			return nil, err
		}
		out = append(out, dep)
	}
	return out, nil
}

// OthersOnConnector counts the project's other deployments on targets of
// t's kind through its provider (one account): the last one taken down
// empties what the account holds for the project.
func (s *Deployments) OthersOnConnector(ctx context.Context, dep *Deployment, t *Target) (int64, error) {
	return deploymentsql.New(s.db.SQL()).OtherDeploymentsOnConnector(ctx, deploymentsql.OtherDeploymentsOnConnectorParams{ProjectID: dep.ProjectID, Excluding: dep.ID, Kind: t.Kind, Provider: t.Provider})
}

// RestoredPlacement is a restored copy's placement: a temporary deployment
// of its source's data class on the source's target, starting from a set.
type RestoredPlacement struct {
	ProjectID, TargetID, BackupID ids.ID
	Name, DataClass               string
	ExpiresAt, At                 time.Time
}

// PlaceRestoredCopy records a restored copy's placement row (kind
// restored-copy), which starts from its set and expires with it.
func (s *Deployments) PlaceRestoredCopy(ctx context.Context, p RestoredPlacement) (*Deployment, error) {
	return deploymentRecord(deploymentsql.New(s.db.SQL()).InsertRestoredCopyDeployment(ctx, deploymentsql.InsertRestoredCopyDeploymentParams{ID: ids.New(p.At), ProjectID: p.ProjectID,
		Name: p.Name, TargetID: p.TargetID, DataClass: p.DataClass, ExpiresAt: sql.NullTime{Time: p.ExpiresAt, Valid: true}, BackupID: p.BackupID, At: p.At}))
}

// ExtendRestoredCopyIn moves a restored copy's placement's expiry in the
// caller's transaction, the one that extends the copy's own record.
func ExtendRestoredCopyIn(ctx context.Context, tx *database.Tx, deploymentID ids.ID, until time.Time) error {
	_, err := deploymentsql.New(tx.SQL()).ExtendRestoredCopyDeployment(ctx, deploymentsql.ExtendRestoredCopyDeploymentParams{ID: deploymentID, ExpiresAt: sql.NullTime{Time: until, Valid: true}})
	return err
}
