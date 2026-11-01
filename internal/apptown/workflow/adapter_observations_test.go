package workflow

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"

	"github.com/flokkit/apptown/internal/database"
)

func TestChangeRecordBatchAndUpliftSelectionKeepScope(t *testing.T) {
	t.Parallel()
	q, run := storedChangeRunFixture(t, workflowsql.InsertChangeParams{ID: ids.New(time.Now()), CreatedAt: time.Now(), Type: "uplift", State: ChangeInProgress.String()})
	ctx := context.Background()
	other := workflowTestProject(t, q.db, "private", "Private", "")
	foreignChange, err := NewCreator(q.db, nil).Create(ctx, ChangeDraft{ProjectID: other, Type: "uplift", Title: "secret"}, "creator")
	if err != nil {
		t.Fatal(err)
	}
	foreign := foreignChange.ID
	if err := requireRunWrite(workflowsql.New(q.db.SQL()).AttachUpliftTarget(ctx, workflowsql.AttachUpliftTargetParams{ID: run.ChangeID, ProjectID: run.ProjectID, TargetCandidateJson: []byte(`{"n":9007199254740993}`)})); err != nil {
		t.Fatal(err)
	}
	s := newService(q.db, allowProject(run.ProjectID), Runtime{})
	changes, err := s.ChangeRecords(ctx, ids.Nil, []ids.ID{run.ChangeID, run.ChangeID, foreign, idstest.N(99999)})
	if err != nil || len(changes) != 1 || changes[0].ID != run.ChangeID || !strings.Contains(changes[0].TargetCandidateJSON, "9007199254740993") {
		t.Fatalf("records %+v: %v", changes, err)
	}
	counts, err := s.ProjectActivity(ctx, run.ProjectID)
	if err != nil || counts[run.ProjectID].OpenUplifts != 1 {
		t.Fatalf("open uplift %+v: %v", counts, err)
	}
	{
		current, err := workflowsql.New(q.db.SQL()).ChangeByID(context.Background(), workflowsql.ChangeByIDParams{ID: run.ChangeID})
		if err != nil {
			t.Fatal(err)
		}
		if rows, err := workflowsql.New(q.db.SQL()).TransitionChange(context.Background(), workflowsql.TransitionChangeParams{At: time.Now(), ID: run.ChangeID, ProjectID: current.ProjectID, OldState: current.State, OldRunID: current.RunID, State: "shipped", RunID: current.RunID, Branch: current.Branch}); err != nil || rows != 1 {
			t.Fatalf("stored change transition: rows=%d err=%v", rows, err)
		}
	}
	selected, err := PendingUpliftAdoptionsIn(ctx, q.db)
	if err != nil || len(selected) != 1 || selected[0] != (ChangeRef{ID: run.ChangeID, ProjectID: run.ProjectID}) {
		t.Fatalf("selection %+v: %v", selected, err)
	}
	if err := q.db.InTx(ctx, func(tx *database.Tx) error {
		return StampUpliftAdoptionIn(ctx, tx, UpliftAdoption{Change: ChangeRef{ID: run.ChangeID, ProjectID: run.ProjectID}, Revision: 7})
	}); err != nil {
		t.Fatal(err)
	}
	selected, err = PendingUpliftAdoptionsIn(ctx, q.db)
	if err != nil || len(selected) != 0 {
		t.Fatalf("adopted selection %+v: %v", selected, err)
	}
	if _, err := s.ChangeRecords(ctx, ids.Nil, []ids.ID{ids.Nil}); err == nil {
		t.Fatal("invalid Change ID accepted")
	}
	dbtest.FailNextStatement(t, q.db, "-- name: VisibleChanges")
	if _, err := s.ChangeRecords(ctx, ids.Nil, []ids.ID{run.ChangeID}); !errors.Is(err, dbtest.ErrInjectedFailure) {
		t.Fatalf("hidden batch failure: %v", err)
	}
}

func TestRunningWorkflowCountRetainsNameProjectAndLifecycle(t *testing.T) {
	t.Parallel()
	q, run, _ := activityFixture(t)
	ctx := context.Background()
	for _, name := range []string{run.WorkflowName, "other"} {
		n, err := RunningWorkflowCountIn(ctx, q.db, run.ProjectID, name)
		want := 0
		if name == run.WorkflowName {
			want = 1
		}
		if err != nil || n != want {
			t.Fatalf("%s count %d: %v", name, n, err)
		}
	}
	n, err := RunningWorkflowCountIn(ctx, q.db, idstest.N(999999), run.WorkflowName)
	if err != nil || n != 0 {
		t.Fatalf("foreign count %d: %v", n, err)
	}
	{
		current, err := workflowsql.New(q.db.SQL()).RunByID(context.Background(), workflowsql.RunByIDParams{ID: run.ID})
		if err != nil {
			t.Fatal(err)
		}
		if rows, err := workflowsql.New(q.db.SQL()).TransitionRun(context.Background(), workflowsql.TransitionRunParams{At: time.Now(), ID: run.ID, ExpectedStatus: current.Status, CurrentStage: current.CurrentStage, RetryRevision: current.RetryRevision, NextStage: current.CurrentStage, NextStatus: "done"}); err != nil || rows != 1 {
			t.Fatalf("stored run transition: rows=%d err=%v", rows, err)
		}
	}
	n, err = RunningWorkflowCountIn(ctx, q.db, run.ProjectID, run.WorkflowName)
	if err != nil || n != 0 {
		t.Fatalf("completed count %d: %v", n, err)
	}
	dbtest.FailNextStatement(t, q.db, "SELECT COUNT(*) FROM runs")
	if _, err := RunningWorkflowCountIn(ctx, q.db, run.ProjectID, run.WorkflowName); !errors.Is(err, dbtest.ErrInjectedFailure) {
		t.Fatalf("hidden count failure: %v", err)
	}
}
