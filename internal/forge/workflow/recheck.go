package workflow

import (
	"context"
	"database/sql"
	"encoding/json"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
)

// All stale-check resets and receipts form one record. A failed write leaves
// the previous verdicts intact and does not request background execution.
func (s reworkStore) refresh(ctx context.Context, RunID ids.ID) (bool, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	q := workflowsql.New(tx.SQL())
	projectID, err := q.RunProjectMeta(ctx, RunID)
	if err != nil {
		return false, database.Missing(err)
	}
	if _, err := q.ProjectForUpdate(ctx, projectID); err != nil {
		return false, err
	}
	run, err := runRecord(workflowsql.New(tx.SQL()).RunForUpdate(ctx, RunID))
	if err != nil || run.Status != RunRunning {
		return false, err
	}
	if run.ProjectID != projectID {
		return false, ErrRunMoved
	}
	work, err := q.LatestFinishedAgentWork(ctx, workflowsql.LatestFinishedAgentWorkParams{RunID: RunID, Stage: run.CurrentStage})
	if err != nil {
		return false, err
	}
	if work.Busy || !work.HasLatestWork {
		return false, nil
	}
	steps, err := stepRecords(q.StaleChecksForUpdate(ctx, workflowsql.StaleChecksForUpdateParams{RunID: RunID, Stage: run.CurrentStage, FinishedAt: sql.NullTime{Time: work.LatestWork, Valid: true}}))
	if err != nil {
		return false, err
	}
	for _, step := range steps {
		if step.Type == "approval" {
			if err := resetApprovalIn(ctx, tx, step); err != nil {
				return false, err
			}
		} else {
			if err := recheckStepIn(ctx, tx, step); err != nil {
				return false, err
			}
		}
		if err := recordReceiptIn(ctx, tx, RunID, "step.reverified", recheckReceipt{step.Name, step.Attempt, "verdict predates reworked output", step.Status, step.Evidence}, s.now()); err != nil {
			return false, err
		}
	}
	if len(steps) == 0 {
		return false, nil
	}
	if err := invalidateRunDecisionIn(ctx, tx, run, s.now()); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

type recheckReceipt struct {
	Step             string          `json:"step"`
	Attempt          int64           `json:"attempt"`
	Reason           string          `json:"reason"`
	PreviousStatus   StepState       `json:"previousStatus"`
	PreviousEvidence json.RawMessage `json:"previousEvidence"`
}

func recheckStepIn(ctx context.Context, tx *database.Tx, step *Step) error {
	if _, err := step.Status.Transition(MoveStepReset, StatusPending); err != nil {
		return err
	}
	return requireAttemptWrite(workflowsql.New(tx.SQL()).RecheckStep(ctx, workflowsql.RecheckStepParams{ID: step.ID, Attempt: step.Attempt}))
}
