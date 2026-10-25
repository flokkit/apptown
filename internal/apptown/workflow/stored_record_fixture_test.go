package workflow

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

// Stored-record fixtures deliberately omit admission's readiness, ownership and
// event ledger. Read-model and transaction tests need those records independently
// of the operation under test. Ordinary execution setup uses admitWorkflowSpec.
// These helpers accept only the owner's existing generated mutation parameters.
func insertStoredRun(t *testing.T, db *database.DB, in workflowsql.InsertRunParams) *Run {
	t.Helper()
	if in.InputsJson == nil {
		in.InputsJson = json.RawMessage(`{}`)
	}
	if in.CreatedAt.IsZero() {
		in.CreatedAt = time.Now().UTC()
	}
	if in.UpdatedAt.IsZero() {
		in.UpdatedAt = in.CreatedAt
	}
	if in.ID.IsZero() {
		in.ID = ids.New(in.CreatedAt)
	}
	// The run's number comes from its project's counter, as the owner's.
	number, err := workflowsql.New(db.SQL()).NextRunNumber(context.Background(), in.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	in.Number = number
	run, err := runRecord(workflowsql.New(db.SQL()).InsertRun(context.Background(), in))
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func insertStoredStep(t *testing.T, db *database.DB, in workflowsql.InsertStageStepParams) ids.ID {
	t.Helper()
	if in.ConfigJson == nil {
		in.ConfigJson = json.RawMessage(`{}`)
	}
	step, err := workflowsql.New(db.SQL()).InsertStageStep(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	return step.ID
}

// A call relationship's evidence is supplied independently of admission. This
// existing guarded mutation replaces status/evidence without finishing a running
// row; it does not cast an approval or append an approval receipt.
func recordStoredCallEvidence(t *testing.T, db *database.DB, id ids.ID, attempt int64, evidence json.RawMessage) {
	t.Helper()
	if err := requireAttemptWrite(workflowsql.New(db.SQL()).RecordApproval(context.Background(), workflowsql.RecordApprovalParams{At: time.Now(),
		ID: id, Attempt: attempt, ExpectedStatus: StatusRunning.String(), Status: StatusRunning.String(), EvidenceJson: evidence,
	})); err != nil {
		t.Fatal(err)
	}
}

// Projection tests need a call's persisted attempt identity without dispatch
// receipts or a clock value. StartParentCall already accepts the prior identity
// and nullable timestamp; each guarded write is checked before using the row.
func insertStoredCall(t *testing.T, db *database.DB, runID ids.ID, stage, name string, attempt int64, evidence json.RawMessage) ids.ID {
	t.Helper()
	id := insertStoredStep(t, db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: runID, Stage: stage, Name: name, Type: "workflow/call", Status: StatusRunning.String()})
	if evidence == nil {
		evidence = json.RawMessage(`{}`)
	}
	for previous := int64(0); previous < attempt; previous++ {
		if err := requireAttemptWrite(workflowsql.New(db.SQL()).StartParentCall(context.Background(), workflowsql.StartParentCallParams{ID: id, Attempt: previous, OldStatus: StatusRunning.String(), Status: StatusRunning.String(), EvidenceJson: evidence})); err != nil {
			t.Fatal(err)
		}
	}
	if attempt == 0 {
		recordStoredCallEvidence(t, db, id, attempt, evidence)
	}
	return id
}

// storedReadyStart supplies only the persisted readiness fact to an independent
// storage fixture. These callers deliberately construct historical stage/attempt
// shapes and omit admission's ledger to test exact settlement or recovery writes;
// Create/Prepared would add unrelated stages and receipts to those assertions.
// Ordinary execution fixtures use admission instead. Never use this to repair
// the dedicated missing-start tests or as a production recovery operation.
func storedReadyStart(t *testing.T, db *database.DB, id ids.ID) {
	t.Helper()
	q := workflowsql.New(db.SQL())
	if row, err := q.InsertRunStart(context.Background(), id); err != nil || row.RunID != id || row.Ready {
		t.Fatalf("stored start: %+v %v", row, err)
	}
	if n, err := q.MarkRunReady(context.Background(), id); err != nil || n != 1 {
		t.Fatalf("stored readiness: %d %v", n, err)
	}
}
