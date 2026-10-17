package workflow

import (
	"context"
	"fmt"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
)

type WithdrawApprovals struct {
	ProjectID, ChangeID, RunID ids.ID
	Withdrawal                 ApprovalWithdrawal
}

type approvalWithdrawalReceipt struct {
	StepID ids.ID `json:"stepId"`
	Step   string `json:"step"`
	Stage  string `json:"stage"`
	ApprovalWithdrawal
}

// Remediation admits its control action and holds project/job/item locks before
// composing this Work participant with item/attempt history and the required
// audit. It withdraws current acceptance; it does not cast a vote or abandon a run.
func WithdrawApprovalsIn(ctx context.Context, tx *database.Tx, request WithdrawApprovals, at time.Time) error {
	if request.ProjectID.IsZero() || request.ChangeID.IsZero() || request.RunID.IsZero() {
		return fmt.Errorf("withdrawal requires its project, change and run")
	}
	q := workflowsql.New(tx.SQL())
	if _, err := q.ProjectForUpdate(ctx, request.ProjectID); err != nil {
		return err
	}
	run, err := runRecord(q.RunForUpdate(ctx, request.RunID))
	if err != nil {
		return err
	}
	if run.ProjectID != request.ProjectID || run.ChangeID != request.ChangeID || run.Status != RunRunning {
		return fmt.Errorf("acceptance no longer belongs to the active remediation work")
	}
	steps, err := stepRecords(q.StepsOfTypeForUpdate(ctx, workflowsql.StepsOfTypeForUpdateParams{RunID: run.ID, Type: "approval"}))
	if err != nil {
		return err
	}
	changed := 0
	for _, step := range steps {
		if step.Stage != run.CurrentStage || (step.Status != StatusPending && step.Status != StatusNeedsHuman) {
			continue
		}
		if step.OverrideBy != "" {
			return fmt.Errorf("acceptance has a recorded override")
		}
		cfg, err := ReadApprovalConfig(step.Config)
		if err != nil {
			return err
		}
		document, err := readApprovalDocument(step.Evidence)
		if err != nil {
			return err
		}
		evidence, status, err := document.evidence.withdraw(step.Status, cfg, request.Withdrawal)
		if err != nil {
			return err
		}
		body, err := document.encode(evidence)
		if err != nil {
			return err
		}
		if err := requireAttemptWrite(q.RecordApproval(ctx, workflowsql.RecordApprovalParams{At: at, ID: step.ID, Attempt: step.Attempt, ExpectedStatus: step.Status.String(), Status: status.String(), EvidenceJson: body})); err != nil {
			return err
		}
		if err := recordReceiptIn(ctx, tx, run.ID, "approval.withdrawn", approvalWithdrawalReceipt{step.ID, step.Name, step.Stage, request.Withdrawal}, at); err != nil {
			return err
		}
		changed++
	}
	if changed == 0 {
		return fmt.Errorf("this work no longer has current acceptance to withdraw; refresh its state")
	}
	return nil
}
