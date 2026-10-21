package deployment

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/fault"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/deployment/internal/deploymentsql"
	"flokkit.io/forge/internal/stamp"
)

// A deployment (design/subsystems/deployment.md §2.3) is a project's app
// standing on one target under a name: identity, a kind, the binding, the
// handle the driver owns, generations, and the four attributes that carry the
// nuance (§2.3a). This package owns the record and its rules; AppTown holds
// the doors and the drivers.

// Deployment is one row of the deployments table.
type Deployment struct {
	DeploymentPolicy   DeploymentPolicy `json:"deploymentPolicy,omitempty"` // enforced standing procedure, not persisted
	ID                 ids.ID           `json:"id"`
	ProjectID          ids.ID           `json:"projectId"`
	Kind               Kind             `json:"kind"`
	Name               string           `json:"name"`
	ChangeID           ids.ID           `json:"changeId,omitzero"`
	TargetID           ids.ID           `json:"targetId"`
	Handle             map[string]any   `json:"handle"`
	DesiredGeneration  int64            `json:"desiredGeneration"`
	ObservedGeneration int64            `json:"observedGeneration"`
	Readiness          Readiness        `json:"readiness"`
	Feed               string           `json:"feed"`
	Audience           string           `json:"audience"`
	DataClass          string           `json:"data"`
	PromoteAfter       string           `json:"promoteAfter,omitempty"`
	URL                string           `json:"url"`
	ExpiresAt          stamp.Time       `json:"expiresAt,omitzero"`
	// The built-in host's handle, as columns (the Docker driver reads them).
	HostPort  int    `json:"-"`
	Container string `json:"-"`
	// HostLabel is the name it carries on its host when that predates the
	// identity cutover (its old integer); "" means its UUID (Label).
	HostLabel string `json:"-"`
	// HostPrefix is what its names on a Docker host or Portainer
	// environment begin with, fixed when it was made (docker.Prefix).
	HostPrefix  string `json:"-"`
	ReleaseRun  ids.ID `json:"releaseRun,omitzero"`
	ReleaseSHA  string `json:"releaseSha,omitempty"`
	ImageDigest string `json:"imageDigest,omitempty"`
	UpdatedAt   string `json:"updatedAt"`
	// StartFrom is the set a deployment started from a copy fills its data
	// from with its first release; StartedFrom is when that was done.
	StartFrom   ids.ID     `json:"startFrom,omitzero"`
	StartedFrom stamp.Time `json:"startedFrom,omitzero"`
}

// Placement is the record as a driver is handed it.
//
// A restored copy is always gated: the driver observes and takes it down
// as one, without its secret, which only the start holds (dataprotection, restored_copy.go).
// Label is the name the deployment carries on its host: container labels and
// boot proofs. A deployment made before the identity cutover keeps its old
// integer, which its containers carry; one made after uses its UUID.
func (e *Deployment) Label() deploy.HostLabel {
	if e.HostLabel != "" {
		return deploy.HostLabel(e.HostLabel)
	}
	return deploy.HostLabel(e.ID.String())
}

func (e *Deployment) Placement() deploy.Placement {
	p := deploy.Placement{ID: e.ID, Label: e.Label(), Name: e.Name, Generation: e.ObservedGeneration,
		URL: e.URL, Handle: e.Handle, Container: e.Container, HostPort: e.HostPort, HostPrefix: e.HostPrefix}
	if e.Kind == Restored {
		p.Gate = &deploy.Gate{}
	}
	return p
}

// Protected is derived, never typed (§2.3a): a deployment is protected when
// its audience is outside the project or its data is production.
func (e *Deployment) Protected() bool {
	return e.Audience == "partners" || e.Audience == "public" || e.DataClass == "production"
}

// Attributes are the four of §2.3a plus the ladder position.
type Attributes struct {
	Feed         string `json:"feed"`
	Audience     string `json:"audience"`
	DataClass    string `json:"data"`
	PromoteAfter string `json:"promoteAfter"`
}

// WithDefaults fills what a request left unsaid: a deployment is fed by
// promotion, for the team, holding non-production data, until told
// otherwise.
func (at Attributes) WithDefaults() Attributes {
	if at.Feed == "" {
		at.Feed = "promotion"
	}
	if at.Audience == "" {
		at.Audience = "team"
	}
	if at.DataClass == "" {
		at.DataClass = "non-production"
	}
	return at
}

func (at Attributes) Validate() error {
	switch at.Feed {
	case "landing", "promotion", "schedule", "manual":
	default:
		return fmt.Errorf("feed must be landing, promotion, schedule or manual (got %q)", at.Feed)
	}
	switch at.Audience {
	case "team", "organisation", "partners", "public":
	default:
		return fmt.Errorf("audience must be team, organisation, partners or public (got %q)", at.Audience)
	}
	switch at.DataClass {
	case "synthetic", "non-production", "production":
	default:
		return fmt.Errorf("data must be synthetic, non-production or production (got %q)", at.DataClass)
	}
	return nil
}

var (
	deploymentNameRe      = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}$`)
	ErrDeploymentName     = errors.New("a deployment name is a short lowercase word")
	ErrDeploymentAdmitted = fault.New(fault.Conflict, "the deployment has an admitted generation; changing its target or making it protected requires a migration")
)

// PreviewName is a preview deployment's name: one per change.
func PreviewName(changeID ids.ID) string { return fmt.Sprintf("preview-%d", changeID) }

// Deployments is the supported record API. Lifecycle writes (attempts,
// outcomes, recovery) have their own owners; this is identity, binding and
// attributes.
type Deployments struct {
	db    *database.DB
	clock func() time.Time // AppTown's clock, which stamps a deployment's updates; nil is the real one
}

func (s *Deployments) now() time.Time { return clock.Or(s.clock)().UTC() }

func NewDeployments(db *database.DB) *Deployments { return &Deployments{db: db} }

func deploymentRecord(r deploymentsql.Deployment, err error) (*Deployment, error) {
	if err != nil {
		return nil, err
	}
	e := &Deployment{
		ID:                 r.ID,
		ProjectID:          r.ProjectID,
		Name:               r.Name,
		ChangeID:           r.ChangeID,
		TargetID:           r.TargetID,
		Handle:             jsonMap(string(r.HandleJson)),
		DesiredGeneration:  r.DesiredGeneration,
		ObservedGeneration: r.ObservedGeneration,
		Feed:               r.Feed,
		Audience:           r.Audience,
		DataClass:          r.DataClass,
		PromoteAfter:       r.PromoteAfter,
		URL:                r.Url,
		ExpiresAt:          stamp.Time{NullTime: r.ExpiresAt},
		HostPort:           int(r.HostPort),
		Container:          r.Container,
		HostLabel:          r.HostLabel,
		HostPrefix:         r.HostPrefix,
		ReleaseRun:         r.ReleaseRun,
		ReleaseSHA:         r.ReleaseSha,
		ImageDigest:        r.ImageDigest,
		UpdatedAt:          r.UpdatedAt.UTC().Format(time.RFC3339Nano),
		StartFrom:          r.StartFromBackup,
		StartedFrom:        stamp.Time{NullTime: r.StartedFromAt},
	}
	if err := e.Kind.Scan(r.Kind); err != nil {
		return nil, err
	}
	e.DeploymentPolicy = e.UpdatePolicy()
	if err := e.Readiness.Scan(r.Readiness); err != nil {
		return nil, err
	}
	return e, nil
}

func (s *Deployments) ByID(ctx context.Context, id ids.ID) (*Deployment, error) {
	return deploymentRecord(deploymentsql.New(s.db.SQL()).DeploymentByID(ctx, id))
}

func (s *Deployments) ByName(ctx context.Context, projectID ids.ID, name string) (*Deployment, error) {
	return deploymentRecord(deploymentsql.New(s.db.SQL()).DeploymentByName(ctx, deploymentsql.DeploymentByNameParams{ProjectID: projectID, Name: name}))
}

// ForProject lists a project's deployments, standing first.
func (s *Deployments) ForProject(ctx context.Context, projectID ids.ID) ([]*Deployment, error) {
	rows, err := deploymentsql.New(s.db.SQL()).DeploymentsForProject(ctx, projectID)
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

// LockDeploymentIn reads a deployment under its row lock inside a caller's
// transaction: the serialization point for admission and outcomes.
func LockDeploymentIn(ctx context.Context, tx *database.Tx, id ids.ID) (*Deployment, error) {
	return deploymentRecord(deploymentsql.New(tx.SQL()).DeploymentForUpdate(ctx, id))
}

// EnsureStanding records a standing deployment on a target with its
// attributes, leaving the handle and generations of an existing row alone:
// attributes are settings, the rest is state. Binding and protection are
// fixed once a generation has been admitted; the upsert checks under its
// row lock so concurrent admission cannot slip between a read and an update.
func (s *Deployments) EnsureStanding(ctx context.Context, projectID ids.ID, name string, targetID ids.ID, at Attributes) (*Deployment, error) {
	return database.Transact(ctx, s.db, func(tx *database.Tx) (*Deployment, error) {
		return ensureStandingIn(ctx, tx, projectID, name, targetID, at, s.now())
	})
}

func validateStanding(name string, at Attributes) error {
	if !deploymentNameRe.MatchString(name) {
		return fmt.Errorf("%w (got %q)", ErrDeploymentName, name)
	}
	if strings.HasPrefix(name, "preview") {
		return errors.New("preview is the name of a kind, not of a standing deployment")
	}
	// copy-<n> names a restored copy: its app and cluster are found by that
	// name when it is taken down, so no standing deployment may share it.
	if strings.HasPrefix(name, "copy-") {
		return errors.New("copy-<n> is the name of a restored copy, not of a standing deployment")
	}
	if err := at.Validate(); err != nil {
		return err
	}
	return nil
}

func ensureStandingIn(ctx context.Context, tx *database.Tx, projectID ids.ID, name string, targetID ids.ID, at Attributes, now time.Time) (*Deployment, error) {
	if err := validateStanding(name, at); err != nil {
		return nil, err
	}
	{
		if err := LockAvailableTargetIn(ctx, tx, targetID); err != nil {
			return nil, err
		}
		if err := nameFreeOfDataIn(ctx, tx, projectID, name, targetID); err != nil {
			return nil, err
		}
		e, err := deploymentRecord(deploymentsql.New(tx.SQL()).EnsureStanding(ctx, deploymentsql.EnsureStandingParams{ID: ids.New(now.UTC()),
			ProjectID:    projectID,
			Name:         name,
			Kind:         Standing.String(),
			TargetID:     targetID,
			Feed:         at.Feed,
			Audience:     at.Audience,
			DataClass:    at.DataClass,
			PromoteAfter: at.PromoteAfter,
			UpdatedAt:    now.UTC(),
		}))
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%s: %w", name, ErrDeploymentAdmitted)
		}
		return e, err
	}
}

// RecordPreview records a preview as a row while it runs (§2.3: previews
// are rows, unique by change, outside the ladder), on the given target.
func (s *Deployments) RecordPreview(ctx context.Context, projectID, changeID, targetID ids.ID, container string, hostPort int, url string) error {
	ctx = context.WithoutCancel(ctx) // the preview runs: its row is written though ctx has ended
	now := s.now()
	handle, _ := json.Marshal(map[string]any{"container": container, "hostPort": hostPort})
	_, err := deploymentsql.New(s.db.SQL()).RecordPreview(ctx, deploymentsql.RecordPreviewParams{ID: ids.New(now),
		ProjectID:  projectID,
		Name:       PreviewName(changeID),
		Kind:       Preview.String(),
		ChangeID:   changeID,
		TargetID:   targetID,
		HandleJson: json.RawMessage(string(handle)),
		HostPort:   int64(hostPort),
		Container:  container,
		Url:        url,
		UpdatedAt:  now,
	})
	return err
}

// ForgetPreview drops a change's preview row: the container it named is gone.
func (s *Deployments) ForgetPreview(ctx context.Context, changeID ids.ID) error {
	ctx = context.WithoutCancel(ctx) // the preview is removed: its row goes though ctx has ended
	_, err := deploymentsql.New(s.db.SQL()).ForgetPreview(ctx, deploymentsql.ForgetPreviewParams{Kind: Preview.String(), ChangeID: changeID})
	return err
}

// ErrNameHeldByData refuses a new deployment whose name, on its target,
// still has data an earlier deployment of that name left there (kept where
// the target has no backups, or a delete that failed). A Docker host would
// otherwise mount the old data volume silently. Settling each row released,
// once the data is deleted by hand, frees the name.
var ErrNameHeldByData = fault.New(fault.Conflict, "data from an earlier deployment of this name is still at the target")

// nameFreeOfDataIn refuses to make a deployment under a name whose earlier
// deployment left data at this target. An existing deployment of that name
// (a rebind or a settings change) is not a new one.
func nameFreeOfDataIn(ctx context.Context, tx *database.Tx, projectID ids.ID, name string, targetID ids.ID) error {
	q := deploymentsql.New(tx.SQL())
	if _, err := q.DeploymentByName(ctx, deploymentsql.DeploymentByNameParams{ProjectID: projectID, Name: name}); err == nil {
		return nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	rows, err := q.OpenRetainedDataForName(ctx, deploymentsql.OpenRetainedDataForNameParams{ProjectID: projectID, Deployment: name, TargetID: targetID, OpenState: RetainedOpen.String()})
	if err != nil || len(rows) == 0 {
		return err
	}
	var what []string
	for _, r := range rows {
		what = append(what, r.Need)
	}
	return fmt.Errorf("%w: %s (%s); delete it and mark it deleted under Left behind, or choose another name", ErrNameHeldByData, name, strings.Join(what, ", "))
}
