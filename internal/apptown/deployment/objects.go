package deployment

import (
	"errors"
	"fmt"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/deploy"
)

// The objects' rules (architecture.md §5.6, "The objects"): decisions
// about an attempt, a deployment or a target that need only the object
// and what the caller hands it. They never touch the database; the
// service reads for update, calls them, writes, and records.

// verdict is what settling an attempt means for its records, decided
// before any write.
type verdict struct {
	Outcome   deploymentOutcome // Superseded, AlreadyApplied, Applied or Uncertain
	Next      AttemptState      // the attempt's state after settling
	Changed   bool              // the attempt's row changes
	Readiness Readiness         // the deployment's readiness after, when the attempt is the desired generation
}

// settle decides an attempt's outcome against the deployment it belongs
// to: superseded when the deployment has moved on (an open attempt closes
// as superseded), already applied when this outcome is the one recorded,
// otherwise the transition to done or failed with the deployment's
// readiness settled accordingly.
func (at Attempt) settle(dep *Deployment, status AttemptState, failed bool) (verdict, error) {
	if status == attemptSuperseded || dep.DesiredGeneration != at.Generation {
		v := verdict{Outcome: Superseded, Next: status}
		if status.open() {
			next, err := status.transition(attemptSuperseded)
			if err != nil {
				return verdict{}, err
			}
			v.Next, v.Changed = next, true
		}
		return v, nil
	}
	wanted := outcomeState(failed)
	if status == wanted {
		return verdict{Outcome: alreadyApplied, Next: status}, nil
	}
	next, err := status.transition(wanted)
	if err != nil {
		return verdict{}, fmt.Errorf("%w: attempt %d is %s, received %s", errOutcomeConflict, at.ID, status, wanted)
	}
	readiness, err := dep.Readiness.settled(next)
	if err != nil {
		return verdict{}, err
	}
	return verdict{Outcome: Applied, Next: next, Changed: true, Readiness: readiness}, nil
}

// unsettled decides what an uncertain outcome means: nothing when the
// deployment has moved on or the attempt is already closed; otherwise
// the attempt becomes uncertain once (an attempt already uncertain keeps
// its state) and the deployment's readiness is lost.
func (at Attempt) unsettled(dep *Deployment, status AttemptState) (verdict, error) {
	if status == attemptSuperseded || dep.DesiredGeneration != at.Generation {
		return verdict{Outcome: Superseded, Next: status}, nil
	}
	if !status.open() {
		return verdict{Outcome: alreadyApplied, Next: status}, nil
	}
	next := status
	if status == AttemptRunning {
		var err error
		if next, err = status.transition(AttemptUncertain); err != nil {
			return verdict{}, err
		}
	}
	lost, err := dep.Readiness.lost()
	if err != nil {
		return verdict{}, err
	}
	return verdict{Outcome: outcomeUncertain, Next: next, Changed: true, Readiness: lost}, nil
}

// completes says whether an observation completes the attempt: it must be
// an active observation of the attempt's own generation.
func (at Attempt) completes(o deploy.Observation) error {
	if o.Generation != at.Generation || o.Rollout != "active" {
		return errors.New("deployment completion requires an active observation of the requested generation")
	}
	return nil
}

// bound refuses an attempt prepared for a target the deployment is no
// longer bound to.
func (e *Deployment) bound(targetID ids.ID) error {
	if e.TargetID != targetID {
		return fmt.Errorf("deployment %d target changed during deployment preparation", e.ID)
	}
	return nil
}

// admit is the deployment taking on a new attempt: the next generation and
// the readiness that says one is on its way. A rebuild names the
// generation it selected and is refused when the deployment moved on.
func (e *Deployment) admit(base *int64) (generation int64, readiness Readiness, err error) {
	if base != nil && e.DesiredGeneration != *base {
		return 0, 0, errors.New("deployment generation changed since this rebuild was queued")
	}

	readiness, err = e.Readiness.admitted()
	if err != nil {
		return 0, 0, err
	}
	return e.DesiredGeneration + 1, readiness, nil
}

// retire decides whether a target may be retired and what its status
// becomes: never AppTown's own host; already retired is
// ErrTargetRetired, which callers treat as nothing to do; a target with
// deployments still bound to it (named by the caller) is refused.
func (t *Target) retire(bound string) (TargetState, error) {
	if t.Origin == OriginBuiltIn {
		return t.Status, fmt.Errorf("%w: %s is this AppTown's own host and cannot be retired", errTargetConflict, t.Name)
	}
	next, err := t.Status.retire()
	if err != nil {
		return t.Status, err
	}
	if bound != "" {
		return t.Status, fmt.Errorf("%w: deployments are bound to %s — move or retire them first: %s", errTargetConflict, t.Name, bound)
	}
	return next, nil
}
