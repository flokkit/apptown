package workflow

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
)

func TestWorkflowResetRejectsStaleRevisionAndAttempt(t *testing.T) {
	t.Parallel()
	store, run, agent := dispatchFixture(t)
	transitionStoredStepForTest(t, store.db, agent, StatusPass)
	snapshot, err := readStep(context.Background(), store.db, agent)
	if err != nil {
		t.Fatal(err)
	}
	invalidateStoredRunForTest(t, store.db, run.ID)
	if err := requireAttemptWrite(workflowsql.New(store.db.SQL()).StartParentCall(context.Background(), workflowsql.StartParentCallParams{ID: agent, Attempt: snapshot.Attempt, OldStatus: StatusPass.String(), Status: StatusPass.String(), EvidenceJson: snapshot.Evidence})); err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := invalidateRunDecisionIn(context.Background(), tx, run, time.Now()); !errors.Is(err, ErrRunMoved) {
		t.Fatalf("stale revision accepted: %v", err)
	}
	err = requireAttemptWrite(workflowsql.New(tx.SQL()).InvalidateStep(context.Background(), workflowsql.InvalidateStepParams{ID: agent, Attempt: snapshot.Attempt}))
	if !errors.Is(err, ErrStepAttemptLost) {
		t.Fatalf("stale attempt accepted: %v", err)
	}
	wantStepState(t, store.db, agent, StatusPass)
}
