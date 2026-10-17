package workflow

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
	"flokkit.io/forge/internal/ids"
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
