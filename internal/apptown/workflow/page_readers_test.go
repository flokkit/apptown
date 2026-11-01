package workflow

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
)

func TestProjectActivityUsesScopedBulkCounts(t *testing.T) {
	t.Parallel()
	q, run, _ := activityFixture(t)
	ctx := context.Background()
	change, err := readChange(context.Background(), q.db, run.ChangeID)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := workflowsql.New(q.db.SQL()).TransitionChange(context.Background(), workflowsql.TransitionChangeParams{At: time.Now(), State: change.State.String(), RunID: run.ID, Branch: change.Branch, ID: change.ID, ProjectID: change.ProjectID, OldState: change.State.String(), OldRunID: change.RunID})
	if err != nil || changed != 1 {
		t.Fatalf("link activity change: changed=%d %v", changed, err)
	}
	other := workflowTestProject(t, q.db, "private", "Private", "")
	child, _ := activityChild(t, q.db, run, "child")
	transitionStoredRunForTest(t, q.db, child.ID, "", RunDone)
	counted := &countedQueries{Handle: q.db.SQL()}
	got, err := readProjectActivity(ctx, counted, []ids.ID{run.ProjectID, other})
	if err != nil || len(got) != 2 || counted.reads != 1 {
		t.Fatalf("bulk %+v, reads %d: %v", got, counted.reads, err)
	}
	v := got[run.ProjectID]
	if v.Changes != 1 || v.ActiveItems != 1 || v.RunningRuns != 1 || v.DoneRuns != 0 || v.OpenChanges != 1 || v.OpenUplifts != 0 || !v.LastRunAt.Valid || v.LastChangeTitle != change.Title {
		t.Fatalf("counts %+v", v)
	}
	if got[other].LastRunAt.Valid || got[other].Changes != 0 || got[other].LastChangeTitle != "" {
		t.Fatalf("empty project %+v", got[other])
	}
	// A root completion counts once; a completed child above does not. Zero
	// visibility stays empty, and a requested idle project receives zero counts.
	transitionStoredRunForTest(t, q.db, run.ID, "", RunDone)
	done, err := readProjectActivity(ctx, q.db, []ids.ID{run.ProjectID})
	if err != nil || len(done) != 1 || done[run.ProjectID].DoneRuns != 1 || done[run.ProjectID].ActiveItems != 0 || done[run.ProjectID].RunningRuns != 0 {
		t.Fatalf("root completion: %+v %v", done, err)
	}
	empty, err := readProjectActivity(ctx, q.db, nil)
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty visibility: %+v %v", empty, err)
	}
	transitionStoredRunForTest(t, q.db, run.ID, "", RunRunning)
	s := newService(q.db, allowProject(run.ProjectID), Runtime{})
	visible, err := s.ProjectActivity(ctx, ids.Nil)
	if err != nil || len(visible) != 1 {
		t.Fatalf("visible %+v: %v", visible, err)
	}
	if _, err := s.ProjectActivity(ctx, other); err == nil {
		t.Fatal("foreign project admitted")
	}
	usage, err := s.WorkflowUsage(ctx, run.ProjectID)
	if err != nil || usage[run.WorkflowName].Runs != 1 || usage["child"].Runs != 1 {
		t.Fatalf("usage %+v: %v", usage, err)
	}
	// sql-guarantee: the scoped reader needs a deliberately foreign run while retaining its existing child/step links; no Workflow mutation reassigns project_id.
	completionExec(t, q.db, `UPDATE runs SET project_id=$1,number=number+100000 WHERE id=$2`, other, run.ID)
	visible, err = s.ProjectActivity(ctx, run.ProjectID)
	if err != nil || visible[run.ProjectID].ActiveItems != 0 || visible[run.ProjectID].RunningRuns != 0 {
		t.Fatalf("foreign current Run counted %+v: %v", visible, err)
	}
	dbtest.FailNextStatement(t, q.db, "-- name: ProjectActivityReport")
	if _, err := s.ProjectActivity(ctx, run.ProjectID); !errors.Is(err, dbtest.ErrInjectedFailure) {
		t.Fatalf("hidden failure: %v", err)
	}
}

func TestDashboardPartitionsVisibleCurrentStages(t *testing.T) {
	t.Parallel()
	q, run, step := activityFixture(t)
	ctx := context.Background()
	other := workflowTestProject(t, q.db, "private", "Private", "")
	foreign := admissionTestChange(t, q.db, other, "secret")
	// sql-guarantee: the dashboard scope law needs an existing run linked to a foreign Change; no Workflow mutation reassigns change_id.
	completionExec(t, q.db, `UPDATE runs SET change_id=$1 WHERE id=$2`, foreign, run.ID)
	s := newService(q.db, allowProject(run.ProjectID), Runtime{})
	dashboard, err := s.Dashboard(ctx)
	if err != nil || len(dashboard.PendingApprovals) != 1 || len(dashboard.ActiveRuns) != 0 || dashboard.PendingApprovals[0].ItemTitle != "" {
		t.Fatalf("approval %+v: %v", dashboard, err)
	}
	// sql-guarantee: the dashboard current-stage filter must observe the same step moved to a future stage; step stage is immutable and has no generated mutation.
	completionExec(t, q.db, `UPDATE steps SET stage='future' WHERE id=$1`, step)
	dashboard, err = s.Dashboard(ctx)
	if err != nil || len(dashboard.PendingApprovals) != 0 || len(dashboard.ActiveRuns) != 1 || dashboard.ActiveRuns[0].ItemTitle != "" {
		t.Fatalf("active %+v: %v", dashboard, err)
	}
	// An active run reads in the change page's words, from the one
	// activity classifier, never as its bare stage name.
	activities, err := s.Activities(ctx, []ids.ID{run.ID})
	if err != nil || dashboard.ActiveRuns[0].State == "" || dashboard.ActiveRuns[0].State != activities[run.ID].State || dashboard.ActiveRuns[0].StateDetail != activities[run.ID].Detail {
		t.Fatalf("active run state %+v, activity %+v: %v", dashboard.ActiveRuns[0], activities, err)
	}
	// sql-guarantee: the scoped reader needs a deliberately foreign run while retaining its existing child/step links; no Workflow mutation reassigns project_id.
	completionExec(t, q.db, `UPDATE runs SET project_id=$1,number=number+100000 WHERE id=$2`, other, run.ID)
	dashboard, err = s.Dashboard(ctx)
	if err != nil || dashboard.PendingApprovals == nil || dashboard.ActiveRuns == nil || len(dashboard.ActiveRuns) != 0 {
		t.Fatalf("private %+v: %v", dashboard, err)
	}
	for _, fragment := range []string{"-- name: DashboardPendingApprovals", "-- name: DashboardActiveRuns"} {
		dbtest.FailNextStatement(t, q.db, fragment)
		if _, err := s.Dashboard(ctx); !errors.Is(err, dbtest.ErrInjectedFailure) {
			t.Fatalf("hidden %s failure: %v", fragment, err)
		}
	}
	counted := &countedQueries{Handle: q.db.SQL()}
	if _, err := readDashboard(ctx, counted, []ids.ID{run.ProjectID, other}, nil); err != nil || counted.reads != 3 {
		t.Fatalf("dashboard reads %d: %v", counted.reads, err)
	}
}

func TestProjectActivityShowsTheLatestChangeWithoutRequiringARun(t *testing.T) {
	t.Parallel()
	q, run, _ := activityFixture(t)
	admissionTestChange(t, q.db, run.ProjectID, "Newest change without a run")
	got, err := readProjectActivity(t.Context(), q.db, []ids.ID{run.ProjectID})
	if err != nil || got[run.ProjectID].LastChangeTitle != "Newest change without a run" {
		t.Fatalf("last change: %+v %v", got, err)
	}
}
