package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
	"flokkit.io/forge/internal/ids"
)

func settlementFixture(t *testing.T, spec schema.Spec) (*database.DB, *Run) {
	t.Helper()
	db, change := changeEditFixture(t, ChangeDraft{Title: "A change"})
	run := insertStoredRun(t, db, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: change.ProjectID, ChangeID: change.ID, WorkflowName: "test", WorkflowVersion: 1, Kind: "pipeline", Status: RunRunning.String(), CurrentStage: spec.Stages[0].Name, DefinitionJson: json.RawMessage(mustJSON(spec))})
	return db, run
}

type policyAdvanceChecks struct{ db *database.DB }

func (c policyAdvanceChecks) RefreshLaw(ctx context.Context, run *Run) error {
	return projectLaws(c.db).Refresh(ctx, run)
}
func (policyAdvanceChecks) Merge(context.Context, *Run) (MergeResult, error) {
	return MergeResult{Allowed: true}, nil
}

func policyEngine(db *database.DB, completionCalls *int) *advancer {
	return newAdvancer(db, advanceRuntime{
		Stages: NewStages(nil, func(string) bool { return false }), Checks: policyAdvanceChecks{db},
		Completion: completionRecorderFunc(func(context.Context, *database.Tx, CompletedRun) (ReleaseReceipt, error) {
			*completionCalls++
			return ReleaseReceipt{}, nil
		}),
	})
}

func policyStageChecks(t *testing.T, db *database.DB, runID ids.ID, stage string) []Check {
	t.Helper()
	checks, err := readStageChecks(context.Background(), db, runID, stage)
	if err != nil {
		t.Fatal(err)
	}
	return checks
}

func TestWorkflowGateIsRecheckedBeforeSettlement(t *testing.T) {
	t.Parallel()
	for _, complete := range []bool{false, true} {
		for _, change := range []string{"failed", "deleted", "retried", "abandoned"} {
			t.Run(change+map[bool]string{true: " completion", false: " transition"}[complete], func(t *testing.T) {
				t.Parallel()
				completionCalls := 0
				spec := schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "finish", Steps: []schema.Step{{Name: "review", Uses: "approval"}}}}}
				if !complete {
					spec.Stages = append(spec.Stages, schema.Stage{Name: "next"})
				}
				store, run := settlementFixture(t, spec)
				step := insertStoredStep(t, store, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "finish", Name: "review", Type: "approval", Status: StatusPending.String(), ConfigJson: json.RawMessage(`{}`)})
				if n, err := workflowsql.New(store.SQL()).RecordApproval(context.Background(), workflowsql.RecordApprovalParams{At: time.Now(), Status: StatusPass.String(), EvidenceJson: json.RawMessage(`{}`), ID: step, ExpectedStatus: StatusPending.String()}); err != nil || n != 1 {
					t.Fatalf("record passing gate: %d %v", n, err)
				}
				switch change {
				case "failed", "deleted":
					// The engine reads a passing gate; the outcome changes after that
					// read and before settlement, inside the settling transaction.
					outcome := map[string]string{
						// sql-guarantee: The settling run UPDATE trigger changes only the gate status after the engine reads pass; a Go operation or generated query cannot execute at this in-transaction trigger point.
						"failed": "UPDATE steps SET status='fail' WHERE id='%s'",
						// sql-guarantee: The settling run UPDATE trigger deletes the observed gate after the engine reads it; no generated mutation deletes one step and a Go operation cannot run at this trigger point.
						"deleted": "DELETE FROM steps WHERE id='%s'",
					}[change]
					completionExec(t, store, `CREATE FUNCTION race_outcome() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN `+fmt.Sprintf(outcome, step)+`; RETURN NEW; END $$`)
					completionExec(t, store, `CREATE TRIGGER race_outcome BEFORE UPDATE ON runs FOR EACH ROW EXECUTE FUNCTION race_outcome()`)
				case "retried":
					// The engine holds an observation the run has already left behind.
					// sql-guarantee: Law refresh and settlement lockRunSnapshotIn compares the whole Run, including UpdatedAt; InvalidateRunDecision also stamps updated_at and could reject the snapshot without checking retry_revision, so change only retry_revision at this original interference point.
					completionExec(t, store, `UPDATE runs SET retry_revision=retry_revision+1 WHERE id=?`, run.ID)
				case "abandoned":
					// sql-guarantee: Law refresh and settlement lockRunSnapshotIn compares the whole Run, including UpdatedAt; TransitionRun also stamps updated_at and could reject the snapshot without checking status, so change only status at this original interference point.
					completionExec(t, store, `UPDATE runs SET status='abandoned' WHERE id=?`, run.ID)
				}
				out, err := policyEngine(store, &completionCalls).AdvanceRun(context.Background(), run, "test")
				if err != nil && !errors.Is(err, ErrRunMoved) || err == nil && out != nil && out.Advanced {
					t.Fatalf("stale decision advanced: %+v %v", out, err)
				}
				expect(t, len(seenEvents(t, store, run.ID)) == 0, "stale decision left receipts")
				if completionCalls != 0 {
					t.Fatalf("stale gate invoked completion %d times", completionCalls)
				}
				current, err := ReadRunIn(context.Background(), store, run.ID)
				if err != nil || current.CurrentStage != "finish" || current.Status == RunDone {
					t.Fatalf("published stale state: %+v %v", current, err)
				}
			})
		}
	}
}

func TestWorkflowStageReceiptSurvivesInventoryChangesAndMissingSteps(t *testing.T) {
	t.Parallel()
	completionCalls := 0
	spec := schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "finish", Steps: []schema.Step{{Name: "security", Uses: "scan", With: map[string]any{"on": "source", "credential": "private-test-value"}}}, Gate: &schema.Gate{Require: []string{"security"}}}}}
	store, run := settlementFixture(t, spec)
	scanners := []Scanner{{Name: "first", Target: "source"}, {Name: "second", Target: "source"}}
	tx, err := store.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := NewStages(recordedScanners(scanners), func(string) bool { return false }).EnterIn(context.Background(), tx, run.ID, &spec, "finish", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	run, readErr := ReadRunIn(context.Background(), store, run.ID)
	if readErr != nil {
		t.Fatal(readErr)
	}
	var payload string
	for _, e := range seenEvents(t, store, run.ID) {
		if e.Kind == "stage.entered" {
			payload += string(e.PayloadJson)
		}
	}
	if payload == "" {
		t.Fatal("stage entry receipt is missing")
	}
	if strings.Contains(payload, "private-test-value") {
		t.Fatal("configuration escaped into stage identity receipt")
	}
	recordScopeRunSteps(t, store, run.ID, "pass")
	// Neither today's attachment set (the engine now sees no bindings at all)
	// nor deleting an old scanner can reduce the stage's expected checks.
	// sql-guarantee: Delete only the second previously receipted scanner while preserving the first check and stage receipt; DeleteProjectSteps removes the whole inventory and no owner operation deletes an individual admitted check.
	completionExec(t, store, `DELETE FROM steps WHERE run_id=? AND name='security:second'`, run.ID)
	out, err := policyEngine(store, &completionCalls).AdvanceRun(context.Background(), run, "system")
	if err != nil && !errors.Is(err, ErrRunMoved) || err == nil && out != nil && out.Advanced {
		t.Fatalf("lost scanner completed: %+v %v", out, err)
	}
	if checks := policyStageChecks(t, store, run.ID, "finish"); len(checks) != 2 || checks[1].Name != "security:second" {
		t.Fatalf("expectations changed: %+v", checks)
	}
	// A failed read cannot be mistaken for no required checks.
	dbtest.FailNextStatement(t, store, "SELECT COALESCE(payload_json->'checks'")
	out, err = policyEngine(store, &completionCalls).AdvanceRun(context.Background(), run, "system")
	if err == nil || errors.Is(err, ErrRunMoved) {
		t.Fatalf("lost read completed: %+v %v", out, err)
	}
	if completionCalls != 0 {
		t.Fatalf("incomplete gate invoked completion %d times", completionCalls)
	}
}
