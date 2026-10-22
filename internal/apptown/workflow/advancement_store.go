package workflow

import (
	"context"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/database"
)

type advanceStore struct {
	db         *database.DB
	stages     Stages
	completion CompletionRecorder
	placement  Placement
	clocked
}

func (w advanceStore) runByID(ctx context.Context, id ids.ID) (*Run, error) {
	return readRun(ctx, w.db, id)
}
func (w advanceStore) stepsForRunStage(ctx context.Context, id ids.ID, stage string) ([]*Step, error) {
	return readStageSteps(ctx, w.db, id, stage)
}
func (w advanceStore) stageChecks(ctx context.Context, id ids.ID, stage string) ([]Check, error) {
	return readStageChecks(ctx, w.db, id, stage)
}
func (w advanceStore) transition(ctx context.Context, run *Run, next, who string) ([]ReadyStep, error) {
	return database.Transact(ctx, w.db, func(tx *database.Tx) ([]ReadyStep, error) {
		spec, err := settleAdvanceIn(ctx, tx, run, stageSuccessor{Stage: next}, w.placement, w.now())
		if err != nil {
			return nil, err
		}
		if err := recordReceiptIn(ctx, tx, run.ID, "advance.ok", stageAdvanceReceipt{run.CurrentStage, next, who}, w.now()); err != nil {
			return nil, err
		}
		return w.stages.EnterIn(ctx, tx, run.ID, spec, next, w.now())
	})
}

type stageAdvanceReceipt struct {
	From string `json:"from"`
	To   string `json:"to"`
	By   string `json:"by"`
}

func settleAdvanceIn(ctx context.Context, tx *database.Tx, expected *Run, next stageSuccessor, placement Placement, at time.Time) (*schema.Spec, error) {
	run, err := lockCurrentRunIn(ctx, tx, expected)
	if err != nil {
		return nil, err
	}
	spec, err := run.Definition()
	if err != nil {
		return nil, err
	}
	actual, err := nextStage(spec, run.CurrentStage, !run.ParentRunID.IsZero())
	if err != nil {
		return nil, err
	}
	if actual.Completing != next.Completing || (!next.Completing && actual.Stage != next.Stage) {
		return nil, ErrRunMoved
	}
	status, nextStage := RunRunning, next.Stage
	if next.Completing {
		status, nextStage = RunDone, run.CurrentStage
	}
	if err := TransitionRunIn(ctx, tx, run, status, nextStage, at); err != nil {
		return nil, err
	}
	stage, _ := spec.Stage(run.CurrentStage)
	if stage == nil {
		return nil, ErrRunMoved
	}
	checks, err := readStageChecks(ctx, tx, run.ID, run.CurrentStage)
	if err != nil {
		return nil, err
	}
	steps, err := readStageSteps(ctx, tx, run.ID, run.CurrentStage)
	if err != nil {
		return nil, err
	}
	gate, err := evaluateGate(stage, checks, steps, placement)
	if err != nil {
		return nil, err
	}
	if !gate.Passed() {
		return nil, ErrRunMoved
	}
	return spec, nil
}
