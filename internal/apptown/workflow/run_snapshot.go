package workflow

import (
	"bytes"
	"context"

	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
)

func lockCurrentRunIn(ctx context.Context, tx *database.Tx, expected *Run) (*Run, error) {
	run, err := lockRunSnapshotIn(ctx, tx, expected)
	if err != nil {
		return nil, err
	}
	if run.Status != RunRunning {
		return nil, ErrRunMoved
	}
	return run, nil
}

func lockRunSnapshotIn(ctx context.Context, tx *database.Tx, expected *Run) (*Run, error) {
	if expected == nil || expected.ID.IsZero() || expected.ProjectID.IsZero() {
		return nil, ErrRunMoved
	}
	q := workflowsql.New(tx.SQL())
	if _, err := q.ProjectForUpdate(ctx, expected.ProjectID); err != nil {
		return nil, err
	}
	if err := refuseUnlessActive(ctx, tx, expected.ProjectID); err != nil {
		return nil, err
	}
	run, err := runRecord(q.RunForUpdate(ctx, expected.ID))
	if err != nil {
		return nil, err
	}
	if *run != *expected {
		return nil, ErrRunMoved
	}
	return run, nil
}

func SameStep(a, b *Step) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.ID == b.ID && a.RunID == b.RunID && a.Stage == b.Stage && a.Type == b.Type && a.Name == b.Name && a.Attempt == b.Attempt && a.Status == b.Status &&
		a.OverrideBy == b.OverrideBy && a.OverrideReason == b.OverrideReason && a.StartedAt == b.StartedAt && a.FinishedAt == b.FinishedAt && bytes.Equal(a.Config, b.Config) && bytes.Equal(a.Evidence, b.Evidence)
}

// Stage, revision and lifecycle may advance while the observed Work identity
// remains the same. An observation of a reassociated Run is not that result.
func sameRunScope(a, b *Run) bool {
	return a != nil && b != nil && a.ID == b.ID && a.ProjectID == b.ProjectID && a.ChangeID == b.ChangeID && a.ParentRunID == b.ParentRunID && a.ParentStepID == b.ParentStepID && a.ParentStepAttempt == b.ParentStepAttempt
}
