package deployment

import (
	"errors"
	"fmt"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/deploy"
)

// The objects' rules (architecture.md §5.6, "The objects"): decisions
// about an attempt, a deployment or a target that need only the object
// and what the caller hands it. They never touch the database; the
// service reads for update, calls them, writes, and records.

// Verdict is what settling an attempt means for its records, decided
// before any write.
type Verdict struct {
	Outcome   Outcome      // Superseded, AlreadyApplied, Applied or Uncertain
	Next      AttemptState // the attempt's state after settling
	Changed   bool         // the attempt's row changes
	Readiness Readiness    // the deployment's readiness after, when the attempt is the desired generation
}

// Settle decides an attempt's outcome against the deployment it belongs
// to: superseded when the deployment has moved on (an open attempt closes
// as superseded), already applied when this outcome is the one recorded,
// otherwise the transition to done or failed with the deployment's
// readiness settled accordingly.
func (at Attempt) Settle(dep *Deployment, status AttemptState, failed bool) (Verdict, error) {
	if status == AttemptSuperseded || dep.DesiredGeneration != at.Generation {
		v := Verdict{Outcome: Superseded, Next: status}
		if status.Open() {
			next, err := status.Transition(AttemptSuperseded)
			if err != nil {
				return Verdict{}, err
			}
			v.Next, v.Changed = next, true
		}
		return v, nil
	}
	wanted := OutcomeState(failed)
	if status == wanted {
		return Verdict{Outcome: AlreadyApplied, Next: status}, nil
	}
	next, err := status.Transition(wanted)
	if err != nil {
		return Verdict{}, fmt.Errorf("%w: attempt %d is %s, received %s", ErrOutcomeConflict, at.ID, status, wanted)
	}
	readiness, err := dep.Readiness.Settled(next)
	if err != nil {
		return Verdict{}, err
	}
	return Verdict{Outcome: Applied, Next: next, Changed: true, Readiness: readiness}, nil
}

// Unsettled decides what an uncertain outcome means: nothing when the
// deployment has moved on or the attempt is already closed; otherwise
// the attempt becomes uncertain once (an attempt already uncertain keeps
// its state) and the deployment's readiness is lost.
func (at Attempt) Unsettled(dep *Deployment, status AttemptState) (Verdict, error) {
	if status == AttemptSuperseded || dep.DesiredGeneration != at.Generation {
		return Verdict{Outcome: Superseded, Next: status}, nil
	}
	if !status.Open() {
		return Verdict{Outcome: AlreadyApplied, Next: status}, nil
	}
	next := status
	if status == AttemptRunning {
		var err error
		if next, err = status.Transition(AttemptUncertain); err != nil {
			return Verdict{}, err
		}
	}
	lost, err := dep.Readiness.Lost()
	if err != nil {
		return Verdict{}, err
	}
	return Verdict{Outcome: Uncertain, Next: next, Changed: true, Readiness: lost}, nil
}

// Completes says whether an observation completes the attempt: it must be
// an active observation of the attempt's own generation.
func (at Attempt) Completes(o deploy.Observation) error {
	if o.Generation != at.Generation || o.Rollout != "active" {
		return errors.New("deployment completion requires an active observation of the requested generation")
	}
	return nil
}

// Bound refuses an attempt prepared for a target the deployment is no
// longer bound to.
func (e *Deployment) Bound(targetID ids.ID) error {
	if e.TargetID != targetID {
		return fmt.Errorf("deployment %d target changed during deployment preparation", e.ID)
	}
	return nil
}

// Admit is the deployment taking on a new attempt: the next generation and
// the readiness that says one is on its way. A rebuild names the
// generation it selected and is refused when the deployment moved on.
func (e *Deployment) Admit(base *int64) (generation int64, readiness Readiness, err error) {
	if base != nil && e.DesiredGeneration != *base {
		return 0, 0, errors.New("deployment generation changed since this rebuild was queued")
	}
	readiness, err = e.Readiness.Admitted()
	if err != nil {
		return 0, 0, err
	}
	return e.DesiredGeneration + 1, readiness, nil
}

// Retire decides whether a target may be retired and what its status
// becomes: never AppTown's own host; already retired is
// ErrTargetRetired, which callers treat as nothing to do; a target with
// deployments still bound to it (named by the caller) is refused.
func (t *Target) Retire(bound string) (TargetState, error) {
	if t.Origin == OriginBuiltIn {
		return t.Status, fmt.Errorf("%w: %s is this AppTown's own host and cannot be retired", ErrTargetConflict, t.Name)
	}
	next, err := t.Status.Retire()
	if err != nil {
		return t.Status, err
	}
	if bound != "" {
		return t.Status, fmt.Errorf("%w: deployments are bound to %s — move or retire them first: %s", ErrTargetConflict, t.Name, bound)
	}
	return next, nil
}
