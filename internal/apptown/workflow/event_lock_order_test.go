package workflow

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/poll/polltest"
)

func TestQueueNoticeWaitsForProjectBeforeLockingRun(t *testing.T) {
	t.Parallel()
	s, ch, _, _, _ := automatedFixture(t)
	// Stored records isolate the lock order from admission and dispatch.
	run := insertStoredRun(t, s.db, workflowsql.InsertRunParams{ProjectID: ch.ProjectID, ChangeID: ch.ID, WorkflowName: "test", WorkflowVersion: 1, Kind: "pipeline", Status: RunRunning.String(), CurrentStage: "work", DefinitionJson: json.RawMessage(`{}`)})
	step := insertStoredStep(t, s.db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "work", Name: "accept", Type: "approval", Status: StatusPending.String()})
	ctx, cancel := context.WithTimeout(t.Context(), polltest.HangGuard(t))
	defer cancel()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	defer func() { cancel(); _ = tx.Rollback(); workers.Wait() }()
	q := workflowsql.New(tx.SQL())
	if _, err := q.ProjectForUpdate(ctx, ch.ProjectID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	workers.Add(1)
	go func() {
		defer workers.Done()
		done <- RecordNotice(ctx, s.db, Notice{RunID: run.ID, Kind: "adapter.note", Payload: json.RawMessage(`{}`), At: time.Now()})
	}()
	// Observe the notice waiting for this project's lock before asking for
	// the run. If its event insert already locked the run, lockStep deadlocks.
	dbtest.WaitForLock(t, s.db, "-- name: ProjectForUpdate")
	if _, _, err := lockStep(ctx, tx, step); err != nil {
		t.Fatal(err)
	}
	if err := recordEventIn(ctx, tx, run.ID, "step.note", json.RawMessage(`{}`), time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("concurrent notice: %v", err)
	}
	wantRunEventCount(t, s.db, run.ID, 1, kindWith("adapter.note"))
	wantRunEventCount(t, s.db, run.ID, 1, kindWith("step.note"))
}

func TestNonQueueNoticeDoesNotWaitForProject(t *testing.T) {
	t.Parallel()
	q, run, _ := activityFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), polltest.HangGuard(t))
	defer cancel()
	tx, err := q.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := workflowsql.New(tx.SQL()).ProjectForUpdate(ctx, run.ProjectID); err != nil {
		t.Fatal(err)
	}
	if err := RecordNotice(ctx, q.db, Notice{RunID: run.ID, Kind: "adapter.note", Payload: json.RawMessage(`{}`), At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	wantEventCount(t, q.db, 1, kindWith("adapter.note"))
}
