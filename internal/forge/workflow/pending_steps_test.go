package workflow

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
)

func TestPendingStepsUsesOneScopedReadForAnyBatch(t *testing.T) {
	t.Parallel()
	// Three runs: a read per run already differs at three.
	for _, size := range []int{1, 3} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			t.Parallel()
			q, run, first := activityFixture(t)
			refs := []RunRef{{ID: run.ID, ProjectID: run.ProjectID}}
			for i := 1; i < size; i++ {
				change, err := NewCreator(q.db, nil).Create(context.Background(), ChangeDraft{ProjectID: run.ProjectID, Title: fmt.Sprintf("bulk-%d", i)}, "creator")
				if err != nil {
					t.Fatal(err)
				}
				admissions := newRunAdmissions(q.db, NewStages(nil, func(kind string) bool { return kind == "script" }), nil)
				admitted, err := admissions.Create(context.Background(), StartRequest{ProjectID: run.ProjectID, ChangeID: change.ID, WorkflowName: "bulk", InputsJSON: "{}", RequestedBy: "operator"}, PreparedRun{Workflow: DefinitionRef{Name: "bulk", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "current"}}}})
				if err != nil {
					t.Fatal(err)
				}
				if err := admissions.Prepared(context.Background(), admitted.ID); err != nil {
					t.Fatal(err)
				}
				id := admitted.ID
				if _, err := workflowsql.New(q.db.SQL()).InsertStageStep(context.Background(), workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: id, Stage: "old", Name: "selected", Type: "approval", ConfigJson: []byte(`{}`), Status: StatusNeedsHuman.String()}); err != nil {
					t.Fatal(err)
				}
				if _, err := workflowsql.New(q.db.SQL()).InsertStageStep(context.Background(), workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: id, Stage: "current", Name: "lower", Type: "script", ConfigJson: []byte(`{}`), Status: StatusPending.String()}); err != nil {
					t.Fatal(err)
				}
				refs = append(refs, RunRef{ID: id, ProjectID: run.ProjectID})
			}
			refs = append(refs, refs[0], RunRef{ID: run.ID, ProjectID: idstest.N(999999)}, RunRef{ID: idstest.N(100000), ProjectID: run.ProjectID})
			counted := &countedQueries{Handle: q.db.SQL()}
			out, err := PendingStepsIn(context.Background(), counted, refs)
			if err != nil || len(out) != size || counted.reads != 1 {
				t.Fatalf("%d observations, %d reads: %v", len(out), counted.reads, err)
			}
			if out[refs[0]].ID != first || out[refs[0]].State != StatusPending {
				t.Fatalf("first %+v", out[refs[0]])
			}
			for _, ref := range refs[1:size] {
				if out[ref].Name != "selected" || out[ref].State != StatusNeedsHuman {
					t.Fatalf("priority %+v", out[ref])
				}
			}
		})
	}
}

func TestPendingStepsPriorityAndTransactionVisibility(t *testing.T) {
	t.Parallel()
	q, run, pending := activityFixture(t)
	humanRow, err := workflowsql.New(q.db.SQL()).InsertStageStep(context.Background(), workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "old", Name: "human", Type: "approval", ConfigJson: []byte(`{}`), Status: StatusNeedsHuman.String()})
	if err != nil {
		t.Fatal(err)
	}
	runningRow, err := workflowsql.New(q.db.SQL()).InsertStageStep(context.Background(), workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "older", Name: "running", Type: "agent/pi", ConfigJson: []byte(`{}`), Status: StatusRunning.String()})
	if err != nil {
		t.Fatal(err)
	}
	human, running := humanRow.ID, runningRow.ID
	refs := []RunRef{{ID: run.ID, ProjectID: run.ProjectID}}
	q.db.Pool().SetMaxOpenConns(1)
	err = q.db.InTx(context.Background(), func(tx *database.Tx) error {
		for _, id := range []ids.ID{running, human, pending} {
			out, err := PendingStepsIn(context.Background(), tx, refs)
			if err != nil {
				return err
			}
			if len(out) != 1 || out[refs[0]].ID != id {
				t.Fatalf("priority %+v expected %d", out, id)
			}
			{
				current, err := workflowsql.New(tx.SQL()).StepByID(context.Background(), id)
				if err != nil {
					return err
				}
				if err := requireAttemptWrite(workflowsql.New(tx.SQL()).RecordApproval(context.Background(), workflowsql.RecordApprovalParams{At: time.Now(), ID: id, Attempt: current.Attempt, ExpectedStatus: current.Status, Status: StatusPass.String(), EvidenceJson: current.EvidenceJson})); err != nil {
					return err
				}
			}
		}
		out, err := PendingStepsIn(context.Background(), tx, refs)
		if err == nil && len(out) != 0 {
			t.Fatalf("completed steps still outstanding: %+v", out)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestPendingStepsFailsClosedAndHandlesEmptyScopes(t *testing.T) {
	t.Parallel()
	q, run, _ := activityFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if out, err := PendingStepsIn(ctx, q.db, nil); !errors.Is(err, context.Canceled) || out != nil {
		t.Fatalf("cancelled %+v: %v", out, err)
	}
	counted := &countedQueries{Handle: q.db.SQL()}
	if out, err := PendingStepsIn(context.Background(), counted, nil); err != nil || out == nil || len(out) != 0 || counted.reads != 0 {
		t.Fatalf("empty %+v: %v reads %d", out, err, counted.reads)
	}
	dbtest.FailNextStatement(t, q.db, "-- name: PendingStepsForScopes")
	if out, err := PendingStepsIn(context.Background(), q.db, []RunRef{{ID: run.ID, ProjectID: run.ProjectID}}); err == nil || out != nil {
		t.Fatalf("failed read %+v: %v", out, err)
	}
}
