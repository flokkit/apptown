package deployment

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/apptown/deployment/internal/releasesql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/fault"
)

// Removal settlement: a take-down's orchestration (its final copy, its
// provider effects, whether data may go) belongs to data protection; the
// placement it removes and the resources that outlive it are Deployment's.
// SelectRemovalIn is the selection a take-down starts from and asks again
// before it settles; RemoveRecordedIn settles it, rechecking that the
// placement is still the one selected, in the caller's transaction.

var ErrRemovalChanged = fault.New(fault.Conflict, "deployment changed during removal; current records were preserved")

var ErrRemovalBusy = fault.New(fault.Conflict, "deployment has unfinished work; finish or resolve it before removal")

// ErrRestoredCopyDoor: a restored copy is taken down from its source's
// card, by its own rule, not as a deployment.
var ErrRestoredCopyDoor = fault.New(fault.Conflict, "a restored copy is taken down from its source deployment's card")

// RemovalIncompleteError never reports successful local removal. The provider
// may have changed even though its call or the later database settlement failed.
// ProviderAccepted means Teardown returned success, not an external fencing guarantee.
type RemovalIncompleteError struct {
	Cause            error
	ProviderAccepted bool
}

func (e *RemovalIncompleteError) Error() string {
	return fmt.Sprintf("removal was not recorded; inspect the provider before retrying: %v", e.Cause)
}
func (e *RemovalIncompleteError) Unwrap() error { return e.Cause }

// FaultKind: a removal the provider refused, or one that raced a change, a
// vanished record or another removal, is a conflict the caller can resolve by
// reading the deployment again. Any other incomplete removal is unexplained.
// The kind is decided here, before any classified cause it wraps.
func (e *RemovalIncompleteError) FaultKind() fault.Kind {
	if !e.ProviderAccepted || errors.Is(e.Cause, ErrRemovalChanged) || errors.Is(e.Cause, ErrDeploymentMissing) || errors.Is(e.Cause, ErrRemovalBusy) {
		return fault.Conflict
	}
	return fault.Internal
}

func (e *Deployment) removableNow(withProject bool) error {
	if err := e.Removable(); err != nil && !withProject {
		return err
	}
	if e.Readiness == Reconciling || e.Readiness == ReadinessUncertain {
		return ErrRemovalBusy
	}
	return nil
}
func (e *Deployment) matchesRemoval(selected *Deployment, withProject bool) error {
	if e.ID != selected.ID || e.TargetID != selected.TargetID || e.Kind != selected.Kind || e.DesiredGeneration != selected.DesiredGeneration || e.ObservedGeneration != selected.ObservedGeneration || e.Container != selected.Container || e.releaseAttributes() != selected.releaseAttributes() || !reflect.DeepEqual(e.Handle, selected.Handle) {
		return ErrRemovalChanged
	}
	return e.removableNow(withProject)
}

// SelectRemovalIn selects the deployment a take-down removes, in the
// caller's transaction: the project's release lock, then the deployment's
// row lock, and the placement's own refusals (main outside a project's
// deletion, work under way, an open attempt, a release in flight, a
// restored copy, which goes by its own door). withProject is a project's
// deletion, which takes main too.
func SelectRemovalIn(ctx context.Context, tx *database.Tx, projectID ids.ID, name string, withProject bool) (*Deployment, error) {
	if _, err := releasesql.New(tx.SQL()).LockReleaseProject(ctx, projectID); err != nil {
		return nil, err
	}
	q := deploymentsql.New(tx.SQL())
	dep, err := deploymentRecord(q.DeploymentByName(ctx, deploymentsql.DeploymentByNameParams{ProjectID: projectID, Name: name}))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrDeploymentMissing
	}
	if err != nil {
		return nil, err
	}
	dep, err = LockDeploymentIn(ctx, tx, dep.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrDeploymentMissing
	}
	if err != nil {
		return nil, err
	}
	if err := dep.removableNow(withProject); err != nil {
		return nil, err
	}
	if dep.Kind == Restored {
		return nil, ErrRestoredCopyDoor
	}
	// Not from records a restore has not yet confirmed against the provider.
	if err := dep.TakeDownHeldAfterRestore(); err != nil {
		return nil, err
	}
	busy, err := q.HasOpenAttempt(ctx, deploymentsql.HasOpenAttemptParams{DeploymentID: dep.ID, RunningState: AttemptRunning.String(), UncertainState: AttemptUncertain.String()})
	if err != nil {
		return nil, err
	}
	if busy {
		return nil, ErrRemovalBusy
	}
	busy, err = releasesql.New(tx.SQL()).ReleaseInFlight(ctx, releasesql.ReleaseInFlightParams{ProjectID: projectID, DeploymentID: dep.ID, OpenState: Releasing.String()})
	if err != nil {
		return nil, err
	}
	if busy {
		return nil, ErrRemovalBusy
	}
	return dep, nil
}

// RemovalSettlement is what a take-down did, as its settlement records it.
type RemovalSettlement struct {
	// Target is the target's name; By who took it down, with the authority
	// the receipt names.
	Target string
	By     access.Principal
	// Taken is what the provider deleted and left.
	Taken deploy.TakenDown
	// FinalBackup is the final copy's set, 0 when none was taken.
	FinalBackup ids.ID
	// Synthetic says the data was an eval copy's, which goes with no copy.
	Synthetic bool
	// Deleted names the needs whose managed instance the take-down deleted
	// after a checked final copy: they are not retained.
	Deleted map[string]bool
	// NoCopy, when not "", is why a person chose to take it down with no
	// final copy.
	NoCopy string
	// At is the caller's clock: when the deployment was taken down.
	At time.Time
}

// RemoveRecordedIn settles a take-down of selected in the caller's
// transaction. It selects the deployment again by its name, under its
// locks (SelectRemovalIn), and settles only when that is still the
// placement selected before the provider was asked (its identity, target,
// generations, container, attributes and handle), else nothing is changed
// (ErrRemovalChanged). Its need instances and what the target left become
// retained resources, the release it served ends, its row is deleted and
// the receipt is written, together.
func RemoveRecordedIn(ctx context.Context, tx *database.Tx, selected *Deployment, withProject bool, s RemovalSettlement) ([]string, error) {
	current, err := SelectRemovalIn(ctx, tx, selected.ProjectID, selected.Name, withProject)
	if err != nil {
		return nil, err
	}
	if err := current.matchesRemoval(selected, withProject); err != nil {
		return nil, err
	}
	return removeRecordedIn(ctx, tx, current, s)
}

// RemoveRestoredIn settles a restored copy's take-down in the caller's
// transaction: the copy's placement row, locked here, and what it made.
func RemoveRestoredIn(ctx context.Context, tx *database.Tx, id ids.ID, s RemovalSettlement) ([]string, error) {
	current, err := LockDeploymentIn(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if current.Kind != Restored {
		return nil, fmt.Errorf("%s is not a restored copy", current.Name)
	}
	return removeRecordedIn(ctx, tx, current, s)
}

func removeRecordedIn(ctx context.Context, tx *database.Tx, dep *Deployment, s RemovalSettlement) ([]string, error) {
	who := s.By.Name
	retained, err := retainNeedInstancesIn(ctx, tx, dep, who, s.Deleted, s.At)
	if err != nil {
		return nil, err
	}
	left, err := retainStorageIn(ctx, tx, dep, s.Taken.Left)
	if err != nil {
		return nil, err
	}
	stayed, err := retainTakenDownIn(ctx, tx, dep, who, left, s.At)
	if err != nil {
		return nil, err
	}
	retained = append(retained, stayed...)
	if err := takeDownReleaseIn(ctx, tx, dep, s.Target, who, s.At); err != nil {
		return nil, err
	}
	if err := releaseAddressClaimsIn(ctx, deploymentsql.New(tx.SQL()), dep.ID); err != nil {
		return nil, err
	}
	if err := deploymentsql.New(tx.SQL()).ClearRemovedDefault(ctx, dep.ID); err != nil {
		return nil, err
	}
	n, err := deploymentsql.New(tx.SQL()).DeleteDeployment(ctx, dep.ID)
	if err != nil {
		return nil, err
	}
	if n != 1 {
		return nil, errors.New("deployment removal did not delete one record")
	}
	receipt := map[string]any{"target": s.Target, "generation": dep.ObservedGeneration, "deleted": s.Taken.Deleted, "orphans": retained, "finalBackup": s.FinalBackup, "syntheticData": s.Synthetic}
	// Who answered for it: a system binding names the decision it carries
	// out and the person who made it.
	if s.By.Credential != "" {
		authority := map[string]any{"via": s.By.Via, "credential": s.By.Credential}
		if s.By.Person != "" {
			authority["person"] = s.By.Person
		}
		receipt["authority"] = authority
	}
	if s.NoCopy != "" {
		receipt["withoutFinalCopy"] = map[string]any{"chosenBy": who, "because": s.NoCopy, "data": "kept at the provider as retained resources"}
	}
	if err := audit.At(s.At).RecordIn(ctx, tx, who, dep.ProjectID, "deployment.torn-down", dep.Name, receipt); err != nil {
		return nil, err
	}
	return retained, nil
}

// takeDownReleaseIn ends the release a removed deployment was serving: its
// log says who took it down, from where and when (the event's time), so the
// release reads as history, not as live. Releases.Live reads the
// deployment's record, which the removal deletes in the same transaction.
func takeDownReleaseIn(ctx context.Context, tx *database.Tx, dep *Deployment, target, who string, at time.Time) error {
	q := deploymentsql.New(tx.SQL())
	rel, err := q.LatestReleasedForDeployment(ctx, deploymentsql.LatestReleasedForDeploymentParams{DeploymentID: dep.ID, State: Released.String()})
	if found, err := database.Lookup(err); err != nil || !found {
		return err
	}
	return AppendReleaseEvent(ctx, tx, rel.ID, fmt.Sprintf("taken down — %s deleted the deployment %s on %s; it no longer serves this release", who, dep.Name, target), at)
}
