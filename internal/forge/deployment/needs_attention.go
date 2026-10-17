package deployment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/deployment/internal/deploymentsql"
	"flokkit.io/forge/internal/forge/jobs"
)

// A deployment needs attention when its own records contradict each other:
// an attempt still open while the deployment reads settled, or a release
// still releasing after its attempt ended. Completion and release
// settlement commit together, so neither arises from the forge's own
// writes; neither is reconstructed at startup either. Startup marks the
// deployment (readiness uncertain, with a ledger line) and goes on; a
// person presses Check again, which observes the deployment and settles it
// through the usual settlement (Service.CheckAgain).

// Contradiction is a release still releasing after its attempt ended.
type Contradiction struct {
	Attempt   Attempt
	Status    AttemptState
	ReleaseID ids.ID
}

// ContradictorySettlements lists every release still releasing after its
// deployment attempt ended.
func (s *Outcomes) ContradictorySettlements(ctx context.Context) ([]Contradiction, error) {
	rows, err := deploymentsql.New(s.db.SQL()).ContradictorySettlements(ctx, deploymentsql.ContradictorySettlementsParams{
		ReleaseState:    Releasing.String(),
		DoneState:       AttemptDone.String(),
		FailedState:     AttemptFailed.String(),
		SupersededState: AttemptSuperseded.String(),
	})
	if err != nil {
		return nil, err
	}
	out := make([]Contradiction, 0, len(rows))
	for _, r := range rows {
		var status AttemptState
		if err := status.Scan(r.Status); err != nil {
			return nil, fmt.Errorf("deployment attempt %d: %w", r.AttemptID, err)
		}
		out = append(out, Contradiction{Attempt: Attempt{ID: r.AttemptID, DeploymentID: r.DeploymentID, Generation: r.Generation}, Status: status, ReleaseID: r.ReleaseID})
	}
	return out, nil
}

// NeedsAttention marks the deployment of at as needing a person: an attempt
// still running becomes uncertain (it blocks new attempts, as uncertainty
// does) and is queued for observation, and the deployment's readiness
// becomes uncertain whatever it read. The ledger line is written when the
// mark is made, not again on each later start that finds it.
func (s *Outcomes) NeedsAttention(ctx context.Context, at Attempt, cause error) error {
	ctx = context.WithoutCancel(ctx) // an outcome after the effect: recorded though ctx has ended
	now := s.now()
	if cause == nil {
		return errors.New("needing attention requires a reason")
	}
	_, err := database.Transact(ctx, s.db, func(tx *database.Tx) (struct{}, error) {
		dep, status, _, err := lockDeploymentIn(ctx, tx, at)
		if err != nil {
			return struct{}{}, err
		}
		q := deploymentsql.New(tx.SQL())
		changed := false
		if status == AttemptRunning {
			next, err := status.Transition(AttemptUncertain)
			if err != nil {
				return struct{}{}, err
			}
			if _, err := q.UncertainAttempt(ctx, deploymentsql.UncertainAttemptParams{
				Status: next.String(), Terminal: next.Terminal(), ID: at.ID,
				ObservedJson: json.RawMessage(jsonText(map[string]any{"_recovery": map[string]any{"reason": cause.Error(), "at": now}})),
			}); err != nil {
				return struct{}{}, err
			}
			changed = true
		}
		if status.Open() {
			if err := s.followups.EnqueueIn(ctx, tx, jobs.Request{Operation: RecoveryJob, Key: fmt.Sprint(at.ID)}); err != nil {
				return struct{}{}, err
			}
		}
		doubted, err := dep.Readiness.Doubted()
		if err != nil {
			return struct{}{}, err
		}
		if dep.Readiness != doubted {
			if _, err := q.SetReadiness(ctx, deploymentsql.SetReadinessParams{Readiness: doubted.String(), UpdatedAt: now, ID: dep.ID}); err != nil {
				return struct{}{}, err
			}
			changed = true
		}
		if !changed {
			return struct{}{}, nil
		}
		return struct{}{}, audit.At(now).RecordIn(ctx, tx, "forge-system", dep.ProjectID, "deployment.needs-attention", dep.Name, map[string]any{
			"attempt": at.ID, "generation": at.Generation, "reason": cause.Error(), "was": dep.Readiness.String()})
	})
	return err
}

// SettleStranded settles a release left releasing after its attempt ended,
// as its attempt's own settlement would have: outcome is the attempt's
// outcome as now observed (done with an active observation, or failed), and
// the release follows it in the usual release settlement. The deployment's
// readiness, if it was doubted and nothing else is open, is read again from
// what this settles.
func (s *Outcomes) SettleStranded(ctx context.Context, c Contradiction, outcome AttemptState, o deploy.Observation, reason string) (Outcome, error) {
	// A settlement of what was observed: written though the caller stops.
	ctx = context.WithoutCancel(ctx)
	now := s.now()
	return database.Transact(ctx, s.db, func(tx *database.Tx) (Outcome, error) {
		dep, status, _, err := lockDeploymentIn(ctx, tx, c.Attempt)
		if err != nil {
			return "", err
		}
		if status != c.Status || status.Open() {
			return "", fmt.Errorf("%w: attempt %d is now %s", ErrOutcomeConflict, c.Attempt.ID, status)
		}
		in, err := deploymentInputsIn(ctx, tx, c.Attempt)
		if err != nil {
			return "", err
		}
		if in.ReleaseID != c.ReleaseID {
			return "", fmt.Errorf("attempt %d is not release %d's", c.Attempt.ID, c.ReleaseID)
		}
		if err := settleDeploymentReleaseIn(ctx, tx, c.Attempt, dep, in, outcome, o, reason, now); err != nil {
			return "", err
		}
		if dep.Readiness == ReadinessUncertain {
			q := deploymentsql.New(tx.SQL())
			open, err := q.HasOpenAttempt(ctx, deploymentsql.HasOpenAttemptParams{DeploymentID: dep.ID, RunningState: AttemptRunning.String(), UncertainState: AttemptUncertain.String()})
			if err != nil {
				return "", err
			}
			if !open {
				next := ReadinessFailed
				standing := c.Attempt.Generation != dep.ObservedGeneration || outcome == AttemptDone
				if dep.ObservedGeneration == dep.DesiredGeneration && standing {
					next = Reconciled
				}
				if _, err := q.SetReadiness(ctx, deploymentsql.SetReadinessParams{Readiness: next.String(), UpdatedAt: now, ID: dep.ID}); err != nil {
					return "", err
				}
			}
		}
		return Applied, nil
	})
}

// CheckResult is what Check again observed and settled, one line per
// attempt or release it looked at.
type CheckResult struct {
	Deployment string           `json:"deployment"`
	Checked    []RecoveryResult `json:"checked"`
}

var errNothingInDoubt = fault.New(fault.Conflict, "nothing about this deployment is in doubt: it has no uncertain attempt and no release left releasing")

// CheckAgain is a person's repair of a deployment that needs attention. It
// observes the deployment on its target, never deploying anything, and
// settles what it observes through the usual settlement:
//   - an uncertain attempt is observed as recovery observes it: running with
//     the image its boot recorded settles it applied, its container gone
//     settles it failed, and a host that cannot be asked leaves it uncertain;
//   - a release left releasing after its attempt ended follows that attempt:
//     failed or superseded fails it, and done is observed the same way
//     (an attempt of an older generation is superseded by the newer one).
//
// The receipt names who pressed it and what was observed.
func (s *Service) CheckAgain(ctx context.Context, projectID ids.ID, name string) (CheckResult, error) {
	result := CheckResult{Deployment: name}
	if s.authority == nil {
		return result, errors.New("deployment checks are not configured")
	}
	actor, err := s.authority.Require(ctx, access.Permission{Capability: access.CapDeploymentOperate, Resource: access.Resource{Type: "project", ID: projectID.String(), ProjectID: projectID}, Context: access.Context{Route: "deployment.check-again", Consequential: true}})
	if err != nil {
		return result, err
	}
	if actor.Principal.Name == "" {
		return result, errors.New("check caller required")
	}
	dep, err := s.Deployments.ByName(ctx, projectID, name)
	if err != nil {
		return result, err
	}
	stranded, err := s.Outcomes.ContradictorySettlements(ctx)
	if err != nil {
		return result, err
	}
	for _, c := range stranded {
		if c.Attempt.DeploymentID != dep.ID {
			continue
		}
		result.Checked = append(result.Checked, s.checkStranded(ctx, dep, c))
	}
	uncertain, err := deploymentsql.New(s.db.SQL()).UncertainAttemptID(ctx, deploymentsql.UncertainAttemptIDParams{DeploymentID: dep.ID, Status: AttemptUncertain.String()})
	if err != nil {
		return result, err
	}
	if !uncertain.IsZero() {
		r, err := s.Recover(ctx, uncertain)
		if err != nil && r.Outcome == "" {
			r.Outcome, r.Reason = string(Uncertain), err.Error()
		}
		result.Checked = append(result.Checked, r)
	}
	if len(result.Checked) == 0 {
		return result, errNothingInDoubt
	}
	if err := audit.Append(ctx, s.db, audit.At(s.now()).String(), actor.Principal.Name, projectID, "deployment.checked-again", dep.Name, jsonText(map[string]any{"checked": result.Checked})); err != nil {
		return result, fmt.Errorf("recording the check: %w", err)
	}
	return result, nil
}

// checkStranded observes and settles one release left releasing.
func (s *Service) checkStranded(ctx context.Context, dep *Deployment, c Contradiction) RecoveryResult {
	r := RecoveryResult{Attempt: c.Attempt.ID}
	settle := func(outcome AttemptState, o deploy.Observation, reason string) RecoveryResult {
		if _, err := s.Outcomes.SettleStranded(ctx, c, outcome, o, reason); err != nil {
			r.Outcome, r.Reason = "unknown", "settling release: "+err.Error()
			return r
		}
		r.Outcome, r.Reason = map[bool]string{true: "recovered", false: "failed"}[outcome == AttemptDone], reason
		return r
	}
	if c.Status != AttemptDone {
		return settle(c.Status, deploy.Observation{}, fmt.Sprintf("its deployment attempt %d ended %s", c.Attempt.ID, c.Status))
	}
	if c.Attempt.Generation != dep.DesiredGeneration {
		return settle(AttemptSuperseded, deploy.Observation{}, "superseded by a newer deployment generation")
	}
	source := RecoverySourceFor(s.Outcomes, func(ctx context.Context, dep *Deployment) (deploy.Driver, error) {
		t, err := s.Targets.ByID(ctx, dep.TargetID)
		if err != nil {
			return nil, err
		}
		return s.providers.Driver(ctx, t, dep)
	})
	snapshot, err := source.LoadAttempt(ctx, c.Attempt.ID)
	if err != nil {
		r.Outcome, r.Reason = "unknown", err.Error()
		return r
	}
	target, err := source.PrepareObservation(ctx, c.Attempt)
	if err != nil {
		r.Outcome, r.Reason = "unknown", err.Error()
		return r
	}
	o, err := target.Observer.Resume(ctx, c.Attempt, snapshot.Checkpoint, func(string, ...any) {})
	switch {
	case err == nil:
		return settle(AttemptDone, o, "observed serving generation "+strconv.FormatInt(c.Attempt.Generation, 10))
	case deploy.IsTerminal(err):
		return settle(AttemptFailed, deploy.Observation{}, err.Error())
	}
	r.Outcome, r.Reason = "unknown", err.Error()
	return r
}
