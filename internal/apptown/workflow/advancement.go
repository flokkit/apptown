package workflow

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/database"
)

// advanceChecks supplies current policy and admitted SCM effects. Work keeps
// all stage decisions, assertions, snapshots and settlement within its owner.
type advanceChecks interface {
	RefreshLaw(context.Context, *Run) error
	Merge(context.Context, *Run) (MergeResult, error)
}
type advanceRuntime struct {
	stages     stages
	assertions *assertions
	checks     advanceChecks
	completion completionRecorder
	// carry applies an accepted change's acceptance to the approvals waiting
	// in a landing's current stage; nil carries nothing.
	carry func(context.Context, *Run) (bool, error)
	// endNothing closes a change whose run found nothing to change; nil
	// leaves such a run waiting at its approval.
	endNothing func(context.Context, *Run) error
	clocked
}
type advancer struct{ engine advanceEngine }

func newAdvancer(db *database.DB, runtime advanceRuntime) *advancer {
	owner := advanceStore{db, runtime.stages, runtime.completion, runtime.clocked}
	return &advancer{advanceEngine{records: owner, carry: runtime.carry, endNothing: runtime.endNothing,
		refreshLaw: func(ctx context.Context, run *Run) error {
			if err := requireRunReady(ctx, db, run.ID); err != nil {
				return err
			}
			if runtime.checks == nil {
				return fmt.Errorf("current advancement checks are unavailable")
			}
			return runtime.checks.RefreshLaw(ctx, run)
		},
		merge: func(ctx context.Context, run *Run) (MergeResult, error) {
			if runtime.checks == nil {
				return MergeResult{}, fmt.Errorf("merge admission is unavailable")
			}
			return runtime.checks.Merge(ctx, run)
		},
		assert: func(ctx context.Context, id ids.ID) (*Step, error) {
			if runtime.assertions == nil {
				return nil, fmt.Errorf("assertion evaluation is unavailable")
			}
			return runtime.assertions.Evaluate(ctx, Assertion{StepID: id})
		},
		note: func(ctx context.Context, id ids.ID, kind string, in advanceBlockedReceipt) {
			if repeat, err := repeatsRefusal(ctx, db, id, in); err != nil {
				log.Printf("workflow event %s for run %d: %v", kind, id, err)
			} else if repeat {
				return
			}
			if err := db.InTx(ctx, func(tx *database.Tx) error { return recordReceiptIn(ctx, tx, id, kind, in, owner.now()) }); err != nil {
				log.Printf("workflow event %s for run %d: %v", kind, id, err)
			}
		},
	}}
}

func (a *advancer) advanceRun(ctx context.Context, run *Run, who string) (*AdvanceResult, error) {
	return a.engine.advanceRun(ctx, run, who)
}
func (s *Service) Advance(ctx context.Context, id ids.ID) (*AdvanceResult, error) {
	project, actor, err := s.runScope(ctx, id, actionAdvanceRun)
	if err != nil {
		return nil, err
	}
	run, err := readScopedRun(ctx, s.db, id, project)
	if err != nil {
		return nil, err
	}
	out, err := s.advancePrepared(ctx, run, actor.Receipt)
	if err != nil {
		return nil, err
	}
	// A blocked/read-only response has no settlement write to check its scope.
	// Revalidate before exposing checks or a stale-observation fallback to a caller.
	current, err := readScopedRun(ctx, s.db, id, project)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrRunMoved
	}
	if err != nil {
		return nil, err
	}
	if !sameRunScope(current, run) {
		return nil, ErrRunMoved
	}
	return out, nil
}

// AdvanceFrom attempts the gate from the caller's snapshot of the run, so a
// delayed child handoff cannot mistake a newer stage for the one whose call
// just completed. The same door as Advance: whoever the context names
// decides, and what the new stage readies is dispatched.
func (s *Service) AdvanceFrom(ctx context.Context, run *Run) (*AdvanceResult, error) {
	if run == nil {
		return nil, ErrRunMoved
	}
	if _, err := s.require(ctx, operationPermission{Action: actionIdentify}); err != nil {
		return nil, err
	}
	actor, err := s.requireActor(ctx, operationPermission{Action: actionAdvanceRun, ProjectID: run.ProjectID, RunID: run.ID})
	if err != nil {
		return nil, err
	}
	if s.advancer == nil {
		return nil, fmt.Errorf("advancement runtime is unavailable")
	}
	out, err := s.advancer.advanceRun(ctx, run, actor.Receipt)
	s.dispatchAdvanced(run.ID, out, err)
	return out, err
}

// advancePrepared is shared by explicit commands and admitted automatic follow-up.
// Preparation cannot change the scope whose authority was checked.
func (s *Service) advancePrepared(ctx context.Context, run *Run, who string) (*AdvanceResult, error) {
	if s.advancer == nil || s.starter == nil {
		return nil, fmt.Errorf("advancement runtime is unavailable")
	}
	if err := s.starter.prepareScoped(ctx, run); err != nil {
		return nil, err
	}
	current, err := readScopedRun(ctx, s.db, run.ID, run.ProjectID)
	if err != nil {
		return nil, err
	}
	if !sameRunScope(current, run) {
		return nil, ErrRunMoved
	}
	out, err := s.advancer.advanceRun(ctx, current, who)
	s.dispatchAdvanced(run.ID, out, err)
	return out, err
}
