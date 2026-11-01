package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
)

func TestProjectRemovalRefusesRunningAndPreservesOtherProjects(t *testing.T) {
	t.Parallel()
	q, run, step := activityFixture(t)
	event := ids.New(time.Now().UTC())
	_, err := workflowsql.New(q.db.SQL()).InsertRunEvent(context.Background(), workflowsql.InsertRunEventParams{ID: event, RunID: run.ID, At: time.Now().UTC(), Kind: "test.receipt", PayloadJson: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	artifact := storedTextArtifact(t, q.db, run.ID, "report", "report", "", "historical content")
	remove := func() error {
		return q.db.InTx(context.Background(), func(tx *database.Tx) error { return RemoveProjectIn(context.Background(), tx, run.ProjectID) })
	}
	if err := remove(); !errors.Is(err, ErrProjectBusy) {
		t.Fatalf("running removal: %v", err)
	}
	storedStep(t, q.db, step)
	other := workflowTestProject(t, q.db, "other-purge", "Other", "")
	otherRun := insertStoredRun(t, q.db, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: other, WorkflowName: "other", WorkflowVersion: 1, Kind: "pipeline", Status: RunRunning.String(), CurrentStage: "test", DefinitionJson: json.RawMessage(`{}`)}).ID
	otherChange := createScopeChange(t, q.db, ChangeDraft{ProjectID: other, Type: "feature", Title: "preserved"})
	if err := q.db.InTx(context.Background(), func(tx *database.Tx) error {
		return audit.At(time.Now()).RecordIn(context.Background(), tx, "tester", run.ProjectID, "history", "project", map[string]any{})
	}); err != nil {
		t.Fatal(err)
	}
	var receipt ids.ID
	for _, entry := range storedReceipts(t, q.db) {
		if entry.ProjectID == run.ProjectID && entry.Kind == "history" {
			receipt = entry.ID
		}
	}
	wantReceiptCount(t, q.db, 1, inProject(run.ProjectID, receiptKind("history")))
	if n, err := workflowsql.New(q.db.SQL()).TransitionRun(context.Background(), workflowsql.TransitionRunParams{At: time.Now(), NextStage: run.CurrentStage, NextStatus: RunDone.String(), ID: run.ID, ExpectedStatus: run.Status.String(), CurrentStage: run.CurrentStage, RetryRevision: run.RetryRevision}); err != nil || n != 1 {
		t.Fatalf("finish removable run: %d %v", n, err)
	}
	if err := remove(); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		table string
		id    ids.ID
	}{{"steps", step}, {"run_events", event}, {"artifacts", artifact}, {"runs", run.ID}, {"changes", run.ChangeID}} {
		// sql-guarantee: removal proves each exact row across five tables is gone (or kept); events and artifacts have no by-id owner read, and one storage check keeps the five rows uniform.
		completionCount(t, q.db, 0, `SELECT COUNT(*) FROM `+row.table+` WHERE id=$1`, row.id)
	}
	if _, err := workflowsql.New(q.db.SQL()).ProjectByID(context.Background(), run.ProjectID); err != nil {
		t.Fatalf("project row removed: %v", err)
	}
	wantReceiptCount(t, q.db, 1, func(e audit.Entry) bool {
		return e.ID == receipt && e.ProjectID == run.ProjectID && e.Kind == "history"
	})
	storedRun(t, q.db, otherRun)
	storedChange(t, q.db, otherChange)
	if err := remove(); err != nil {
		t.Fatalf("empty participant repeat: %v", err)
	}
}

func TestProjectRemovalRejectsInvalidRunState(t *testing.T) {
	t.Parallel()
	q, run, step := activityFixture(t)
	if n, err := workflowsql.New(q.db.SQL()).TransitionRun(context.Background(), workflowsql.TransitionRunParams{At: time.Now(), NextStage: run.CurrentStage, NextStatus: "invalid", ID: run.ID, ExpectedStatus: run.Status.String(), CurrentStage: run.CurrentStage, RetryRevision: run.RetryRevision}); err != nil || n != 1 {
		t.Fatalf("store invalid run state: %d %v", n, err)
	}
	err := q.db.InTx(context.Background(), func(tx *database.Tx) error { return RemoveProjectIn(context.Background(), tx, run.ProjectID) })
	if err == nil {
		t.Fatal("invalid stored Run state was accepted")
	}
	storedStep(t, q.db, step)
}

func TestProjectRemovalRollsBackFailureAndSuppressedDelete(t *testing.T) {
	for _, fault := range []string{"midway", "suppressed step", "suppressed change"} {
		t.Run(fault, func(t *testing.T) {
			t.Parallel()
			q, run, step := activityFixture(t)
			if n, err := workflowsql.New(q.db.SQL()).TransitionRun(context.Background(), workflowsql.TransitionRunParams{At: time.Now(), NextStage: run.CurrentStage, NextStatus: RunAbandoned.String(), ID: run.ID, ExpectedStatus: run.Status.String(), CurrentStage: run.CurrentStage, RetryRevision: run.RetryRevision}); err != nil || n != 1 {
				t.Fatalf("abandon removable run: %d %v", n, err)
			}
			event := ids.New(time.Now().UTC())
			_, err := workflowsql.New(q.db.SQL()).InsertRunEvent(context.Background(), workflowsql.InsertRunEventParams{ID: event, RunID: run.ID, At: time.Now().UTC(), Kind: "test.receipt", PayloadJson: json.RawMessage(`{}`)})
			if err != nil {
				t.Fatal(err)
			}
			artifact := storedTextArtifact(t, q.db, run.ID, "report", "report", "", "")
			if fault == "midway" {
				dbtest.FailNextStatement(t, q.db, "DELETE FROM run_events")
			} else {
				table := "steps"
				if fault == "suppressed change" {
					table = "changes"
				}
				completionExec(t, q.db, `CREATE FUNCTION veto_purge() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NULL; END $$`)
				completionExec(t, q.db, `CREATE TRIGGER veto_purge BEFORE DELETE ON `+table+` FOR EACH ROW EXECUTE FUNCTION veto_purge()`)
			}
			err = q.db.InTx(context.Background(), func(tx *database.Tx) error { return RemoveProjectIn(context.Background(), tx, run.ProjectID) })
			if fault == "midway" && !errors.Is(err, dbtest.ErrInjectedFailure) {
				t.Fatalf("failure: %v", err)
			}
			if fault != "midway" && (err == nil || !strings.Contains(err.Error(), "project removal left Work")) {
				t.Fatalf("suppressed: %v", err)
			}
			for _, row := range []struct {
				table string
				id    ids.ID
			}{{"steps", step}, {"run_events", event}, {"artifacts", artifact}, {"runs", run.ID}, {"changes", run.ChangeID}} {
				// sql-guarantee: removal proves each exact row across five tables is gone (or kept); events and artifacts have no by-id owner read, and one storage check keeps the five rows uniform.
				completionCount(t, q.db, 1, `SELECT COUNT(*) FROM `+row.table+` WHERE id=$1`, row.id)
			}
		})
	}
}

func TestProjectRemovalHoldsAdmissionLockUntilSettlement(t *testing.T) {
	t.Parallel()
	q, run, _ := activityFixture(t)
	transitionScopeRun(t, q.db, workflowsql.TransitionRunParams{At: time.Now(), ID: run.ID, NextStatus: "done"})
	tx, err := q.db.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := RemoveProjectIn(context.Background(), tx, run.ProjectID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	// Both canonical admissions and the FK of an independent insert contend with
	// the project-first lock. No new Run can appear between purge and root removal.
	_, err = workflowsql.New(q.db.SQL()).InsertRun(ctx, workflowsql.InsertRunParams{ID: ids.New(time.Now().UTC()), ProjectID: run.ProjectID, WorkflowName: "late", WorkflowVersion: 1, Kind: "pipeline", Status: "running", CurrentStage: "test", DefinitionJson: json.RawMessage(`{}`), InputsJson: json.RawMessage(`{}`), CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()})
	if err == nil || ctx.Err() != context.DeadlineExceeded {
		t.Fatalf("admission escaped lock: %v (%v)", err, ctx.Err())
	}
}
