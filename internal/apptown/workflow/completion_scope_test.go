package workflow

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
)

func TestCompletionParentRequiresCurrentChildScope(t *testing.T) {
	t.Parallel()
	for _, mutation := range []string{"parent project", "child project", "child parent", "child step", "child attempt", "child state"} {
		t.Run(mutation, func(t *testing.T) {
			t.Parallel()
			q, parent, _ := activityFixture(t)
			child, step := activityChild(t, q.db, parent, "child")
			{
				current, err := workflowsql.New(q.db.SQL()).RunByID(context.Background(), workflowsql.RunByIDParams{ID: child.ID})
				if err != nil {
					t.Fatal(err)
				}
				if rows, err := workflowsql.New(q.db.SQL()).TransitionRun(context.Background(), workflowsql.TransitionRunParams{At: time.Now(), ID: child.ID, ExpectedStatus: current.Status, CurrentStage: current.CurrentStage, RetryRevision: current.RetryRevision, NextStage: current.CurrentStage, NextStatus: "done"}); err != nil || rows != 1 {
					t.Fatalf("stored run transition: rows=%d err=%v", rows, err)
				}
			}
			{
				current, err := workflowsql.New(q.db.SQL()).StepByID(context.Background(), step)
				if err != nil {
					t.Fatal(err)
				}
				if rows, err := workflowsql.New(q.db.SQL()).RecordApproval(context.Background(), workflowsql.RecordApprovalParams{At: time.Now(), ID: step, Attempt: current.Attempt, ExpectedStatus: current.Status, Status: StatusPass.String(), EvidenceJson: []byte(fmt.Sprintf(`{"childRunId":%q,"outcome":"done"}`, child.ID))}); err != nil || rows != 1 {
					t.Fatalf("stored step state: rows=%d err=%v", rows, err)
				}
			}
			child, err := readRun(context.Background(), q.db, child.ID)
			if err != nil {
				t.Fatal(err)
			}
			store := advanceStore{db: q.db}
			if got, err := store.completedParent(context.Background(), child); err != nil || got == nil {
				t.Fatalf("valid parent: %+v %v", got, err)
			}
			other := workflowTestProject(t, q.db, "other", "Other", "")
			switch mutation {
			case "parent project":
				// sql-guarantee: Move the previously validated parent's immutable project binding and require the same completed-child snapshot to lose its parent.
				completionExec(t, q.db, `UPDATE runs SET project_id=?,number=number+100000 WHERE id=?`, other, parent.ID)
			case "child project":
				// sql-guarantee: Move the previously validated child's immutable project binding after caching its completion scope.
				completionExec(t, q.db, `UPDATE runs SET project_id=?,number=number+100000 WHERE id=?`, other, child.ID)
			case "child parent":
				// sql-guarantee: Erase the child's parent link after a valid completed-parent read; no owner mutation detaches an admitted child.
				completionExec(t, q.db, `UPDATE runs SET parent_run_id=NULL WHERE id=?`, child.ID)
			case "child step":
				// sql-guarantee: No owner mutation erases an admitted child's owning call-step link; invalidate that same cached relationship after its valid read.
				completionExec(t, q.db, `UPDATE runs SET parent_step_id=NULL WHERE id=?`, child.ID)
			case "child attempt":
				if err := requireRunWrite(workflowsql.New(q.db.SQL()).SetParentStepAttempt(context.Background(), workflowsql.SetParentStepAttemptParams{ID: child.ID, ParentStepAttempt: child.ParentStepAttempt + 1})); err != nil {
					t.Fatal(err)
				}
			case "child state":
				{
					current, err := workflowsql.New(q.db.SQL()).RunByID(context.Background(), workflowsql.RunByIDParams{ID: child.ID})
					if err != nil {
						t.Fatal(err)
					}
					if rows, err := workflowsql.New(q.db.SQL()).TransitionRun(context.Background(), workflowsql.TransitionRunParams{At: time.Now(), ID: child.ID, ExpectedStatus: current.Status, CurrentStage: current.CurrentStage, RetryRevision: current.RetryRevision, NextStage: current.CurrentStage, NextStatus: "abandoned"}); err != nil || rows != 1 {
						t.Fatalf("stored run transition: rows=%d err=%v", rows, err)
					}
				}
			}
			if got, err := store.completedParent(context.Background(), child); err != nil || got != nil {
				t.Fatalf("stale %s returned parent %+v: %v", mutation, got, err)
			}
		})
	}
}
