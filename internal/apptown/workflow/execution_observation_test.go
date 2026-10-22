package workflow

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestExecutionObservationUsesCanonicalScopedRecords(t *testing.T) {
	t.Parallel()
	for _, test := range []string{"tracked", "latest landing", "foreign project", "foreign change", "foreign change record", "missing run", "missing landing", "scope failure"} {
		t.Run(test, func(t *testing.T) {
			t.Parallel()
			q, run, step := activityFixture(t)
			insertStoredStep(t, q.db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "later", Name: "ignored", Type: "script", Status: StatusPending.String()})
			evidence := `{"large":9007199254740993,"reason":"observe"}`
			if changed, err := workflowsql.New(q.db.SQL()).RetryStep(context.Background(), workflowsql.RetryStepParams{ConfigJson: []byte(`{"nested":{"keep":true}}`), ID: step, Attempt: 0}); err != nil || changed != 1 {
				t.Fatalf("store observation config: changed=%d %v", changed, err)
			}
			for attempt := int64(0); attempt < 3; attempt++ {
				if err := requireAttemptWrite(workflowsql.New(q.db.SQL()).StartParentCall(context.Background(), workflowsql.StartParentCallParams{ID: step, Attempt: attempt, OldStatus: StatusPending.String(), Status: StatusPending.String(), EvidenceJson: []byte(evidence)})); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := workflowsql.New(q.db.SQL()).InsertRunStart(context.Background(), run.ID); err != nil {
				t.Fatal(err)
			}
			insertRunEventForTest(t, q.db, run.ID, "step.rework", nil)
			in := ExecutionSelection{Change: ChangeRef{ID: run.ChangeID, ProjectID: run.ProjectID}, RunID: run.ID}
			wantID := run.ID
			wantAbsent := false
			switch test {
			case "latest landing":
				insertStoredRun(t, q.db, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: run.ProjectID, ChangeID: run.ChangeID, WorkflowName: "land", WorkflowVersion: 1, Kind: "pipeline", CurrentStage: "finish", ParentRunID: run.ID, DefinitionJson: []byte(`{}`), Status: RunRunning.String()})
				wantID = insertStoredRun(t, q.db, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: run.ProjectID, ChangeID: run.ChangeID, WorkflowName: "land", WorkflowVersion: 1, Kind: "pipeline", CurrentStage: "finish", DefinitionJson: []byte(`{}`), Status: RunRunning.String()}).ID
				in.RunID, in.LatestLanding = ids.Nil, true
			case "foreign project":
				in.Change.ProjectID = idstest.N(999999)
				wantAbsent = true
			case "foreign change":
				in.Change.ID = idstest.N(999999)
				wantAbsent = true
			case "foreign change record":
				project := workflowTestProject(t, q.db, "other", "Other", "")
				// sql-guarantee: the scoped observation must face a Change whose stored project disagrees with its run; no Change mutation permits project reassignment.
				completionExec(t, q.db, `UPDATE changes SET project_id=?,number=number+100000 WHERE id=?`, project, run.ChangeID)
				wantAbsent = true
			case "missing run":
				in.RunID = idstest.N(999999)
				wantAbsent = true
			case "missing landing":
				in.RunID, in.LatestLanding = ids.Nil, true
				wantAbsent = true
			case "scope failure":
				dbtest.FailNextStatement(t, q.db, "FROM runs WHERE id")
			}
			q.db.Pool().SetMaxOpenConns(1)
			var out *ExecutionObservation
			err := q.db.InTx(context.Background(), func(tx *database.Tx) error {
				var err error
				out, err = ObserveExecutionIn(context.Background(), tx, in)
				return err
			})
			if test == "scope failure" {
				if err == nil || out != nil {
					t.Fatalf("failed observation %+v: %v", out, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if wantAbsent {
				if out != nil {
					t.Fatalf("foreign/missing scope observed: %+v", out)
				}
				return
			}
			if out == nil || out.Run.ID != wantID || out.Run.Status != RunRunning {
				t.Fatalf("execution: %+v", out)
			}
			if test == "tracked" {
				if len(out.Steps) != 1 || out.Steps[0].ID != step || out.Steps[0].Attempt != 3 || out.Steps[0].OwnedAttempt() != 0 || !out.Unready || out.Reworks != 1 || !strings.Contains(string(out.Steps[0].Evidence), "9007199254740993") {
					t.Fatalf("canonical observation: %+v", out)
				}
			}
		})
	}
}

func TestExecutionObservationComparisonDetectsChangedRecords(t *testing.T) {
	t.Parallel()
	for _, mutation := range []string{
		// sql-guarantee: Same must detect retry_revision alone; InvalidateRunDecision also changes updated_at, which Same compares.
		`UPDATE runs SET retry_revision=retry_revision+1`,
		// sql-guarantee: Same must detect an isolated definition change on the identical run; definitions are immutable and have no generated update.
		`UPDATE runs SET definition_json='{"changed":true}'`,
		// sql-guarantee: Same must detect an isolated inputs change on the identical run; admitted inputs are immutable and have no generated update.
		`UPDATE runs SET inputs_json='{"changed":true}'`,
		// sql-guarantee: Same must detect current_stage alone; TransitionRun also changes updated_at, which Same compares.
		`UPDATE runs SET current_stage='later'`,
		// sql-guarantee: Same must detect attempt alone; StartParentCall also changes started_at and finished_at, which Same compares.
		`UPDATE steps SET attempt=attempt+1`,
		"step evidence",
		"step config",
		// sql-guarantee: Same must detect override_by alone; OverrideStep also changes status, reason, finish time, and would not isolate this field.
		`UPDATE steps SET override_by='operator'`,
		"run start",
		"rework event",
	} {
		t.Run(mutation, func(t *testing.T) {
			t.Parallel()
			q, run, stepID := activityFixture(t)
			read := func() *ExecutionObservation {
				t.Helper()
				out, err := database.Transact(context.Background(), q.db, func(tx *database.Tx) (*ExecutionObservation, error) {
					return ObserveExecutionIn(context.Background(), tx, ExecutionSelection{Change: ChangeRef{ID: run.ChangeID, ProjectID: run.ProjectID}, RunID: run.ID})
				})
				if err != nil {
					t.Fatal(err)
				}
				return out
			}
			original := read()
			if !original.Same(*read()) {
				t.Fatal("unchanged read differs")
			}
			switch mutation {
			case "step evidence":
				changed, err := workflowsql.New(q.db.SQL()).RecordApproval(context.Background(), workflowsql.RecordApprovalParams{At: time.Now(), Status: StatusPending.String(), EvidenceJson: []byte(`{"n":9007199254740993}`), ID: stepID, Attempt: 0, ExpectedStatus: StatusPending.String()})
				if err != nil || changed != 1 {
					t.Fatalf("change evidence: changed=%d %v", changed, err)
				}
			case "step config":
				changed, err := workflowsql.New(q.db.SQL()).RetryStep(context.Background(), workflowsql.RetryStepParams{ConfigJson: []byte(`{"decision":true}`), ID: stepID, Attempt: 0})
				if err != nil || changed != 1 {
					t.Fatalf("change config: changed=%d %v", changed, err)
				}
			case "run start":
				if _, err := workflowsql.New(q.db.SQL()).InsertRunStart(context.Background(), run.ID); err != nil {
					t.Fatal(err)
				}
			case "rework event":
				insertRunEventForTest(t, q.db, run.ID, "step.rework", nil)
			default:
				completionExec(t, q.db, mutation)
			}
			if original.Same(*read()) {
				t.Fatal("changed execution accepted")
			}
		})
	}
}
