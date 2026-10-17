package workflow

import (
	"context"
	"errors"
	"time"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
)

var ErrRunMoved = errors.New("workflow stage or status changed")

// TransitionRunIn is the only update of a persisted run's lifecycle. The
// conditional write checks the snapshot's state, stage and decision revision
// against the current row. Its caller owns the required receipts and commit.
// This never updates the caller's detached snapshot or returns partial success.
// at is the forge's time of the move.
func TransitionRunIn(ctx context.Context, tx *database.Tx, expected *Run, next RunState, stage string, at time.Time) error {
	state, err := expected.Status.Transition(next)
	if err != nil {
		return err
	}
	n, err := workflowsql.New(tx.SQL()).TransitionRun(ctx, workflowsql.TransitionRunParams{
		ID: expected.ID, CurrentStage: expected.CurrentStage, RetryRevision: expected.RetryRevision,
		ExpectedStatus: expected.Status.String(), NextStage: stage, NextStatus: state.String(), At: at,
	})
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrRunMoved
	}
	return nil
}
