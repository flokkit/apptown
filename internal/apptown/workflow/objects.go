package workflow

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/apptown/schema"
)

// The values own the rules. A run judges its own stage, a change moves by
// named acts and refuses the wrong ones, a step judges a vote against its
// own configuration. None of them touch the database: the service loads,
// asks, saves what was said, and runs the effects it named.

// --- Run ------------------------------------------------------------------------------

// advancement is what a run says should happen next, given what is recorded.
type advancement struct {
	Required []string // checks the stage declares
	Missing  []string // required checks that were never recorded
	Blocked  []*Step  // steps holding the gate
	Merge    bool     // the move needs the source merged first
	Complete bool     // the last stage passed: the run is done
	Next     string   // the stage to enter, when neither blocked nor complete
}

// passed: the gate let the run through, to a merge, the next stage or completion.
func (a advancement) passed() bool { return len(a.Missing) == 0 && len(a.Blocked) == 0 }

// advance judges the run's current stage: are the declared checks recorded
// and passing, and if so what follows. Only a running run can be judged.
func (run *Run) advance(steps []*Step, recorded []Check) (advancement, error) {
	if run.Status != RunRunning {
		return advancement{}, fmt.Errorf("run %d is %s, not running", run.ID, run.Status)
	}
	spec, stage, err := run.currentStage()
	if err != nil {
		return advancement{}, err
	}
	gate, err := evaluateGate(stage, recorded, steps)
	if err != nil {
		return advancement{}, err
	}
	a := advancement{Required: gate.Required, Missing: gate.Missing, Blocked: gate.Blocking}
	if !a.passed() {
		return a, nil
	}
	next, err := nextStage(spec, run.CurrentStage, !run.ParentRunID.IsZero())
	if err != nil {
		return advancement{}, err
	}
	a.Merge, a.Complete, a.Next = next.Merge, next.Completing, next.Stage
	return a, nil
}

// currentStage is the run's pinned definition and the stage it is in.
func (run *Run) currentStage() (*schema.Spec, *schema.Stage, error) {
	spec, err := run.Definition()
	if err != nil {
		return nil, nil, err
	}
	stage, _ := spec.Stage(run.CurrentStage)
	if stage == nil {
		return nil, nil, fmt.Errorf("run %d: current stage %q not in definition", run.ID, run.CurrentStage)
	}
	return spec, stage, nil
}

// decides: a decision on the step is one this run is waiting for, in the
// stage it is in now.
func (run *Run) decides(step *Step) error {
	if run.Status != RunRunning || step.RunID != run.ID || step.Stage != run.CurrentStage {
		return errStepDecisionConflict
	}
	return nil
}

// --- Change ----------------------------------------------------------------------------

// apply moves the change by the act, or refuses: the state machine and what
// a run attachment needs. The caller checks that a named run is the change's
// own root work.
func (c changeLifecycle) apply(move ChangeMove) (changeLifecycle, error) {
	switch move.Action {
	case attachChangeRun, LandChange, resumeChange:
		if move.RunID.IsZero() {
			return c, fmt.Errorf("Change work attachment needs a run")
		}
	default:
		if !move.RunID.IsZero() {
			return c, fmt.Errorf("this Change act cannot attach a run")
		}
	}
	next, err := c.State.transition(move.Action)
	if err != nil {
		return c, err
	}
	if move.Action == cancelChangeLanding && next == c.State {
		return c, nil // nothing to cancel
	}
	out := c
	out.State = next
	if !move.RunID.IsZero() {
		out.RunID = move.RunID
	}
	return out, nil
}

// --- Step ------------------------------------------------------------------------------

// approvalOutcome is a vote's effect: the round after it, the evidence to
// save, the step's status, and whether the round is still open.
type approvalOutcome struct {
	Round    ApprovalEvidence
	Evidence json.RawMessage
	Status   StepState
	Pending  bool
}

// approve judges a vote against the step's own configuration and the round
// so far: only an approval step, only one not yet decided or overridden,
// one vote per person, quorum from distinct votes, a rejection closes the
// round. It does not know whether an agent is still revising the work.
// Approve casts vote; at is AppTown's time of the vote, used when the
// vote carries none.
func (s *Step) approve(vote ApprovalVote, at time.Time) (approvalOutcome, error) {
	if s.Type != "approval" {
		return approvalOutcome{}, fmt.Errorf("step %q is not an approval step: %w", s.Name, errStepApprovalInput)
	}
	if s.OverrideBy != "" {
		return approvalOutcome{}, errStepDecisionConflict
	}
	if s.Status == StatusPass || s.Status == StatusFail {
		return approvalOutcome{}, fmt.Errorf("this approval is already decided: %w", errStepDecisionConflict)
	}
	cfg, err := ReadApprovalConfig(s.Config)
	if err != nil {
		return approvalOutcome{}, err
	}
	document, err := readApprovalDocument(s.Evidence)
	if err != nil {
		return approvalOutcome{}, err
	}
	if vote.At == "" {
		vote.At = at.UTC().Format(time.RFC3339Nano)
	}
	round, status, pending, err := document.evidence.cast(s.Status, cfg, vote)
	if err != nil {
		return approvalOutcome{}, err
	}
	body, err := document.encode(round)
	if err != nil {
		return approvalOutcome{}, err
	}
	return approvalOutcome{Round: round, Evidence: body, Status: status, Pending: pending}, nil
}

// override passes the step on a person's say-so, with their reason: only a
// step not already overridden, and only from a state an override may leave.
func (s *Step) override(by, reason string) (StepState, error) {
	if strings.TrimSpace(reason) == "" {
		return 0, errOverrideReason
	}
	if strings.TrimSpace(by) == "" {
		return 0, errStepDecisionCaller
	}
	if s.OverrideBy != "" {
		return 0, errStepDecisionConflict
	}
	next, err := s.Status.transition(moveStepOverride, StatusPass)
	if err != nil {
		return 0, errStepDecisionConflict
	}
	return next, nil
}
