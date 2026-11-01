package workflow

import (
	"context"
	"errors"
	"time"

	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
)

var ErrRunMoved = errors.New("workflow stage or status changed")

// TransitionRunIn is the only update of a persisted run's lifecycle. The
// conditional write checks the snapshot's state, stage and decision revision
// against the current row. Its caller owns the required receipts and commit.
// This never updates the caller's detached snapshot or returns partial success.
// at is AppTown's time of the move.
func TransitionRunIn(ctx context.Context, tx *database.Tx, expected *Run, next RunState, stage string, at time.Time) error {
	state, err := expected.Status.transition(next)
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
