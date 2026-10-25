package deployment

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/apptown/deployment/internal/releasesql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

// After a whole-AppTown restore (stock/docs/guides/apptown-backups.md), the
// records of a deployment on App Platform or Portainer say what ran when the
// backup was taken, not what runs now: the provider may have been released
// to since, or the app taken away. Until the provider has been read, such a
// deployment is held: nothing acts on it from those records, including work
// the backup's database held queued. Each minute the provider is asked,
// through the observation the driver already makes:
//
//   - it cannot be read: still held, asked again;
//   - the app is gone: held as gone, for a person to decide (take it down);
//   - it runs what the records say: confirmed, the hold lifted;
//   - it runs something else: adopted, the hold lifted. A release with the
//     origin observed-after-restore records what runs, never as a release
//     AppTown shipped, and the recorded release can still be deployed again.
//
// A deployment on a Docker host is not held: the built-in host's are rebuilt
// with their data (dataprotection's fill), and an attached host's keep
// running as they are.

// RestoreCheck is a deployment's state after a restore: "" when it is not
// held.
type RestoreCheck string

const (
	RestoreUnconfirmed RestoreCheck = "unconfirmed"
	RestoreGone        RestoreCheck = "gone"
	// RestoreFilling is a built-in deployment whose rebuild puts its data
	// back (dataprotection's fill): only that rebuild runs it, and its end
	// lifts the hold.
	RestoreFilling RestoreCheck = "filling"
)

// ReleaseOrigin says whether AppTown shipped a release.
const (
	OriginShipped              = "shipped"
	OriginObservedAfterRestore = "observed-after-restore"
)

var (
	// ErrUnconfirmedSinceRestore refuses an effect on a deployment whose
	// provider has not been read since AppTown was restored.
	ErrUnconfirmedSinceRestore = fault.New(fault.Conflict, "not yet confirmed since AppTown was restored: AppTown acts on it once its provider says what it runs")
	// ErrGoneSinceRestore refuses an effect other than a take-down on a
	// deployment its provider no longer has.
	ErrGoneSinceRestore = fault.New(fault.Conflict, "gone from its provider since AppTown was restored: take it down, or release it again once AppTown can rebuild it")
	// ErrFillingSinceRestore refuses anything but its rebuild for a built-in
	// deployment waiting for its data.
	ErrFillingSinceRestore = fault.New(fault.Conflict, "waiting for its data since AppTown was restored: its rebuild puts the data back before the app starts")
)

// HeldAfterRestore refuses any effect on a held deployment.
func (e *Deployment) HeldAfterRestore() error {
	switch e.RestoreCheck {
	case RestoreUnconfirmed:
		return fmt.Errorf("%s is %w", e.Name, ErrUnconfirmedSinceRestore)
	case RestoreGone:
		return fmt.Errorf("%s is %w", e.Name, ErrGoneSinceRestore)
	case RestoreFilling:
		return fmt.Errorf("%s is %w", e.Name, ErrFillingSinceRestore)
	}
	return nil
}

// TakeDownHeldAfterRestore refuses a take-down of a deployment not yet
// confirmed: it would delete from records that may be stale. A gone one
// may be taken down: that removes AppTown's records of it.
func (e *Deployment) TakeDownHeldAfterRestore() error {
	if e.RestoreCheck == RestoreUnconfirmed {
		return fmt.Errorf("%s is %w", e.Name, ErrUnconfirmedSinceRestore)
	}
	return nil
}

// RestoreFiller is data protection's side of a filling deployment's
// rebuild: the fill its boot runs after the needs and before anything of
// the app (docker.BootRequest.Fill), and the settling of that fill once the
// rebuild has ended (why "" when its data is back).
type RestoreFill struct {
	Run    func(network string) error
	Copies map[string]deploy.StorageCopy
}

type RestoreFiller interface {
	Fill(ctx context.Context, dep *Deployment, t *Target) (RestoreFill, error)
	Filled(ctx context.Context, deploymentID ids.ID, why string) error
}

// HoldForFill holds built-in deployments whose rebuild puts their data
// back, in the restored database before AppTown first starts.
func (s *Deployments) HoldForFill(ctx context.Context, deploymentIDs []ids.ID) error {
	q := deploymentsql.New(s.db.SQL())
	for _, id := range deploymentIDs {
		if _, err := q.SettleRestoreCheck(ctx, deploymentsql.SettleRestoreCheckParams{Next: string(RestoreFilling), UpdatedAt: s.now(), ID: id, Current: ""}); err != nil {
			return err
		}
	}
	return nil
}

// settleFill ends a filling deployment's wait once its rebuild has ended,
// however it ended: its fill is settled (why "" when the data is back) and
// the hold lifted, so a deployment never stays held behind a rebuild that
// will not come. A failure to record it is logged; the hold then stays
// for the next start's rebuild.
func (s *Service) settleFill(ctx context.Context, dep *Deployment, why string) {
	if dep.RestoreCheck != RestoreFilling {
		return
	}
	ctx = context.WithoutCancel(ctx)
	if s.filler != nil {
		if err := s.filler.Filled(ctx, dep.ID, why); err != nil {
			log.Printf("settling %s's fill after the restore: %v", dep.Name, err)
			return
		}
	}
	kind, facts := "deployment.filled-after-restore", map[string]any{}
	if why != "" {
		kind, facts["why"] = "deployment.not-filled-after-restore", why
	}
	if err := s.settleHold(ctx, dep, RestoreFilling, kind, facts); err != nil {
		log.Printf("lifting %s's hold after the restore: %v", dep.Name, err)
	}
}

// RebuildRecorded says whether a restored AppTown can rebuild dep from its
// record: the attempt that put its observed generation there recorded
// its inputs.
func (s *Service) RebuildRecorded(ctx context.Context, dep *Deployment) bool {
	if dep.ObservedGeneration == 0 {
		return false
	}
	_, err := s.Outcomes.InputsAtGeneration(ctx, dep.ID, dep.ObservedGeneration)
	return err == nil
}

// heldAfterRestoreIn refuses a new release of a held deployment at once,
// in the release's admission (attempt admission refuses it again).
func heldAfterRestoreIn(ctx context.Context, tx *database.Tx, deploymentID ids.ID) error {
	if deploymentID.IsZero() {
		return nil
	}
	dep, err := deploymentRecord(deploymentsql.New(tx.SQL()).DeploymentByID(ctx, deploymentID))
	if found, err := database.Lookup(err); err != nil || !found {
		return err
	}
	return dep.HeldAfterRestore()
}

// heldKinds are the targets whose deployments a restore holds.
var heldKinds = []string{KindDOApp, KindPortainer}

// Held is a deployment a restore held.
type Held struct {
	ID        ids.ID
	ProjectID ids.ID
	Name      string
}

// HoldAfterRestore holds every standing deployment on App Platform or
// Portainer, in the restored database before AppTown first starts.
func (s *Deployments) HoldAfterRestore(ctx context.Context) ([]Held, error) {
	rows, err := deploymentsql.New(s.db.SQL()).HoldStandingAfterRestore(ctx, deploymentsql.HoldStandingAfterRestoreParams{
		Held: string(RestoreUnconfirmed), UpdatedAt: s.now(), Standing: Standing.String(), Kinds: heldKinds})
	if err != nil {
		return nil, err
	}
	out := make([]Held, len(rows))
	for i, r := range rows {
		out[i] = Held{ID: r.ID, ProjectID: r.ProjectID, Name: r.Name}
	}
	return out, nil
}

// CheckAfterRestore asks the provider of each held deployment what it runs
// and settles what it can. One deployment's failure is logged and the
// others go on. A deployment with an attempt open waits for it to end.
func (s *Service) CheckAfterRestore(ctx context.Context) error {
	held, err := deploymentsql.New(s.db.SQL()).DeploymentsHeldAfterRestore(ctx, string(RestoreUnconfirmed))
	if err != nil {
		return err
	}
	for _, id := range held {
		if err := s.checkAfterRestore(ctx, id); err != nil {
			log.Printf("checking a deployment after the restore: %v", err)
		}
	}
	return nil
}

func (s *Service) checkAfterRestore(ctx context.Context, id ids.ID) error {
	dep, err := s.Deployments.ByID(ctx, id)
	if err != nil || dep.RestoreCheck != RestoreUnconfirmed {
		return err
	}
	if dep.DesiredGeneration != dep.ObservedGeneration {
		return nil // an attempt is open: its recovery settles it first
	}
	t, err := s.Targets.ByID(ctx, dep.TargetID)
	if err != nil {
		return err
	}
	d, err := s.providers.Driver(ctx, t, dep)
	if err != nil {
		return fmt.Errorf("%s: %w", dep.Name, err)
	}
	record, ok := d.(deploy.RunningRecord)
	if !ok {
		return fmt.Errorf("%s: its target's provider cannot say what runs; it stays held", dep.Name)
	}
	o, err := d.Observe(ctx, dep.Placement())
	if err != nil {
		return nil // not readable: still held, asked again
	}
	released, err := releasesql.New(s.db.SQL()).LatestReleasedRef(ctx, releasesql.LatestReleasedRefParams{DeploymentID: dep.ID, Released: Released.String()})
	if _, err := database.Lookup(err); err != nil {
		return err
	}
	recorded := record.RecordedRunning(dep.Placement(), released)
	switch {
	case o.Rollout == "absent" && o.Running == "":
		return s.settleRestoreCheck(ctx, dep, RestoreGone, "deployment.gone-after-restore", map[string]any{"recorded": recorded})
	case o.Running == "":
		return nil // nothing named as running yet (a first rollout): asked again
	case o.Running == recorded:
		return s.settleRestoreCheck(ctx, dep, "", "deployment.confirmed-after-restore", map[string]any{"running": o.Running})
	}
	return s.adoptAfterRestore(ctx, dep, t, record.AdoptRunning(o.Running), recorded, o.Running)
}

func (s *Service) settleRestoreCheck(ctx context.Context, dep *Deployment, next RestoreCheck, kind string, facts map[string]any) error {
	return s.settleHoldTo(ctx, dep, RestoreUnconfirmed, next, kind, facts)
}

// settleHold lifts a hold of the kind current.
func (s *Service) settleHold(ctx context.Context, dep *Deployment, current RestoreCheck, kind string, facts map[string]any) error {
	return s.settleHoldTo(ctx, dep, current, "", kind, facts)
}

func (s *Service) settleHoldTo(ctx context.Context, dep *Deployment, current, next RestoreCheck, kind string, facts map[string]any) error {
	now := s.now()
	return s.db.InTx(ctx, func(tx *database.Tx) error {
		n, err := deploymentsql.New(tx.SQL()).SettleRestoreCheck(ctx, deploymentsql.SettleRestoreCheckParams{
			Next: string(next), UpdatedAt: now, ID: dep.ID, Current: string(current)})
		if err != nil || n != 1 {
			return err // settled meanwhile: nothing to say
		}
		return audit.At(now).RecordIn(ctx, tx, "apptown-system", dep.ProjectID, kind, dep.Name, facts)
	})
}

// adoptAfterRestore records what the provider runs as an observed release
// and lifts the hold. The driver's change to the handle makes the running
// one the record (on App Platform, so a later take-down's check of the
// provider's selection matches what runs).
func (s *Service) adoptAfterRestore(ctx context.Context, dep *Deployment, t *Target, handle map[string]any, recorded, running string) error {
	now := s.now()
	patch, err := json.Marshal(handle)
	if err != nil || handle == nil {
		patch = []byte("{}")
	}
	return s.db.InTx(ctx, func(tx *database.Tx) error {
		if err := LockProjectIn(ctx, tx, dep.ProjectID); err != nil {
			return err
		}
		n, err := deploymentsql.New(tx.SQL()).AdoptAfterRestore(ctx, deploymentsql.AdoptAfterRestoreParams{
			HandlePatch: patch, Next: "", UpdatedAt: now, ID: dep.ID, Current: string(RestoreUnconfirmed)})
		if err != nil || n != 1 {
			return err // settled meanwhile
		}
		number, err := releasesql.New(tx.SQL()).RecordObservedRelease(ctx, releasesql.RecordObservedReleaseParams{
			ID: ids.New(now), ProjectID: dep.ProjectID, State: Released.String(), Deployment: dep.Name, DeploymentID: dep.ID,
			Generation: dep.ObservedGeneration, ExternalRef: running, At: now, Origin: OriginObservedAfterRestore,
			Notes: fmt.Sprintf("observed on %s after AppTown was restored; AppTown did not ship this (it had recorded %s)", t.Name, orNone(recorded)),
		})
		if err != nil {
			return err
		}
		return audit.At(now).RecordIn(ctx, tx, "apptown-system", dep.ProjectID, "deployment.adopted-after-restore", dep.Name,
			map[string]any{"release": number, "recorded": recorded, "running": running, "target": t.Name})
	})
}

// handBundle is the bundle a landing-fed deployment takes by hand
// (DeployableByHandAt): the one it is known to run, or, once a restore
// adopted what its provider ran (its newest released release is observed,
// not shipped), the one AppTown last shipped there: deploying the recorded
// version puts back what AppTown's records said.
func (s *Service) handBundle(ctx context.Context, dep *Deployment) (ids.ID, error) {
	running, err := s.runningBundle(ctx, dep)
	if err != nil || !running.IsZero() {
		return running, err
	}
	q := releasesql.New(s.db.SQL())
	origin, err := q.NewestReleasedOrigin(ctx, releasesql.NewestReleasedOriginParams{DeploymentID: dep.ID, Released: Released.String()})
	if found, err := database.Lookup(err); err != nil || !found || origin != OriginObservedAfterRestore {
		return ids.Nil, err
	}
	bundle, err := q.NewestShippedBundle(ctx, releasesql.NewestShippedBundleParams{DeploymentID: dep.ID, Released: Released.String(), Shipped: OriginShipped})
	if found, err := database.Lookup(err); err != nil || !found {
		return ids.Nil, err
	}
	return bundle, nil
}

func orNone(s string) string {
	if s == "" {
		return "nothing"
	}
	return s
}
