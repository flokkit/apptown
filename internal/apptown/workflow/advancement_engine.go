package workflow

import (
	"context"
	"errors"
	"log"

	"github.com/flokkit/apptown/internal/ids"
)

// advanceRecords is the engine's persistence boundary. The engine cannot
// query unrelated AppTown tables or start external work inside a transaction.
type advanceRecords interface {
	runByID(context.Context, ids.ID) (*Run, error)
	stepsForRunStage(context.Context, ids.ID, string) ([]*Step, error)
	stageChecks(context.Context, ids.ID, string) ([]Check, error)
	transition(context.Context, *Run, string, string) ([]ReadyStep, error)
	complete(context.Context, *Run) error
	retryCompletion(context.Context, *Run) (bool, error)
}

// advanceEngine owns the decision and ordering of run advancement. AppTown
// supplies policy/assertion checks and cross-domain effects in workflow_wiring.go;
// neither App nor a raw database is available to the engine.
type advanceEngine struct {
	records    advanceRecords
	refreshLaw func(context.Context, *Run) error
	assert     func(context.Context, ids.ID) (*Step, error)
	merge      func(context.Context, *Run) (MergeResult, error)
	note       func(context.Context, ids.ID, string, advanceBlockedReceipt)
	carry      func(context.Context, *Run) (bool, error)
	// endNothing closes a change whose run found nothing to change, instead
	// of asking anyone to accept it (Advancement.NothingToChange); nil ends
	// nothing.
	endNothing func(context.Context, *Run) error
}

// allowed is separate from blocking: a merge remains refused even when the
// adapter could not retrieve the check that explains the refusal.
type MergeResult struct {
	Allowed  bool
	Blocking *Step
}

// AdvanceResult reports what happened on an advance attempt.
type AdvanceResult struct {
	Ready            []ReadyStep `json:"-"`
	Advanced         bool        `json:"advanced"`
	RunStatus        RunState    `json:"runStatus"`
	Stage            string      `json:"stage"`
	Blocking         []*Step     `json:"blocking"`
	Required         []string    `json:"required"`
	Missing          []string    `json:"missing"`
	FollowUpsPending bool        `json:"followUpsPending"`
}

// advanceRun retains the caller's stage and retry revision through the final
// conditional write. Delayed child handoffs must not reread a newer stage and
// mistake it for the stage whose call just completed.
func (w advanceEngine) advanceRun(ctx context.Context, run *Run, who string) (*AdvanceResult, error) {
	if run == nil {
		return nil, ErrRunMoved
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Keep the admitted observation independent of its caller and returned rows.
	snapshot := *run
	run = &snapshot
	runID := run.ID
	res := &AdvanceResult{RunStatus: run.Status, Stage: run.CurrentStage}
	if run.Status != RunRunning {
		if run.Status == RunDone {
			// An explicit advance of a completed run can retry its recorded
			// follow-up work, without recording completion a second time.
			pending, err := w.records.retryCompletion(ctx, run)
			if err != nil {
				return nil, err
			}
			res.FollowUpsPending = pending
		}
		return res, nil
	}
	// A run whose definition or stage cannot be read gets no effects at all.
	if _, _, err := run.currentStage(); err != nil {
		return nil, err
	}
	// Clear a standing law pause if the law has relaxed, before reading gates.
	if err := w.refreshLaw(ctx, run); err != nil {
		if errors.Is(err, ErrRunMoved) {
			return w.currentResult(ctx, run)
		}
		return nil, err
	}
	// An accepted change's acceptance may answer this stage's approvals, once
	// its other steps have (acceptance_carry.go). The gate below reads the
	// votes it cast.
	if w.carry != nil {
		if _, err := w.carry(ctx, run); err != nil && !errors.Is(err, ErrRunMoved) {
			log.Printf("run %d: carrying the acceptance into stage %s: %v", runID, run.CurrentStage, err)
		}
	}
	steps, err := w.records.stepsForRunStage(ctx, runID, run.CurrentStage)
	if err != nil {
		return nil, err
	}
	for i, step := range steps {
		if step.isAssertion() && step.Status != StatusPass {
			updated, err := w.assert(ctx, step.ID)
			if err != nil {
				return nil, err
			}
			steps[i] = updated
		}
	}

	recorded, err := w.records.stageChecks(ctx, runID, run.CurrentStage)
	if err != nil {
		return nil, err
	}
	// The run judges; the engine records the judgement and does what it named.
	decision, err := run.advance(steps, recorded)
	if err != nil {
		return nil, err
	}
	res.Required, res.Missing, res.Blocking = decision.Required, decision.Missing, decision.Blocked
	if _, ok := decision.nothingToChange(steps); ok && w.endNothing != nil && !run.ChangeID.IsZero() && run.ParentRunID.IsZero() {
		if err := w.endNothing(ctx, run); err == nil {
			res.Advanced, res.Blocking, res.RunStatus = true, nil, RunAbandoned
			return res, nil
		} else if !errors.Is(err, ErrRunMoved) {
			// The approval keeps waiting: a person can still close it.
			log.Printf("run %s: ending a change with nothing to change: %v", runID, err)
		}
	}
	if !decision.passed() {
		blockedBy := make([]string, len(decision.Blocked))
		for i, s := range decision.Blocked {
			blockedBy[i] = s.Name
		}
		w.note(ctx, runID, "advance.blocked", advanceBlockedReceipt{run.CurrentStage, blockedBy, decision.Missing, who})
		return res, nil
	}
	if decision.Merge {
		merge, err := w.merge(ctx, run)
		if err != nil {
			if errors.Is(err, ErrRunMoved) {
				return w.currentResult(ctx, run)
			}
			return nil, err
		}
		if !merge.Allowed {
			if merge.Blocking != nil {
				res.Blocking = append(res.Blocking, merge.Blocking)
			}
			return res, nil
		}
	}
	if decision.Complete {
		if err := w.records.complete(ctx, run); err != nil {
			if errors.Is(err, ErrRunMoved) {
				return w.currentResult(ctx, run)
			}
			return nil, err
		}
		res.Advanced = true
		res.RunStatus = RunDone
		res.FollowUpsPending = true
		return res, nil
	}
	next := decision.Next
	auto, err := w.records.transition(ctx, run, next, who)
	if err != nil {
		if errors.Is(err, ErrRunMoved) {
			return w.currentResult(ctx, run)
		}
		return nil, err
	}
	// transition returns only after the new stage and every step commit.
	res.Ready = auto
	res.Advanced = true
	res.Stage = next
	if w.carry != nil && len(auto) == 0 {
		// Nothing automatic will advance the new stage, so give the acceptance
		// its chance now. The stage is committed: a failure to carry leaves
		// its approval waiting for a person rather than failing the advance.
		entered, err := w.records.runByID(ctx, runID)
		if err != nil {
			return res, nil
		}
		carried, err := w.carry(ctx, entered)
		if err != nil && !errors.Is(err, ErrRunMoved) {
			log.Printf("run %d: carrying the acceptance into stage %s: %v", runID, next, err)
		}
		if carried {
			again, err := w.advanceRun(ctx, entered, who)
			if err != nil {
				return nil, err
			}
			again.Advanced = true
			return again, nil
		}
	}
	return res, nil
}

func (w advanceEngine) currentResult(ctx context.Context, expected *Run) (*AdvanceResult, error) {
	run, err := w.records.runByID(ctx, expected.ID)
	if err != nil {
		return nil, err
	}
	if !sameRunScope(run, expected) {
		return nil, ErrRunMoved
	}
	return &AdvanceResult{RunStatus: run.Status, Stage: run.CurrentStage}, nil
}

type advanceBlockedReceipt struct {
	Stage    string   `json:"stage"`
	Blocking []string `json:"blocking"`
	Missing  []string `json:"missing"`
	By       string   `json:"by"`
}
