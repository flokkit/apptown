package forge

import (
	"context"
	"fmt"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/deployment"
	"flokkit.io/forge/internal/forge/jobs"
	"flokkit.io/forge/internal/forge/workflow"
)

const workflowCompletionJob = "workflow-completion"

// workflowCompletionEffects records a completed run's release; now is the
// forge's clock, which stamps it (nil is the real one).
type workflowCompletionEffects struct{ now func() time.Time }

func (w workflowCompletionEffects) RecordCompletionIn(ctx context.Context, tx *database.Tx, in workflow.CompletedRun) (workflow.ReleaseReceipt, error) {
	var receipt workflow.ReleaseReceipt
	if in.Release {
		release, err := deployment.CreateReleaseIn(ctx, tx, deployment.ReleaseDraft{Source: deployment.ReleaseFromWorkflow, ProjectID: in.ProjectID, RunID: in.RunID, ChangeID: in.ChangeID, Deployment: "main", Actor: in.ApprovedBy, At: clock.Or(w.now)()})
		if err != nil {
			return receipt, err
		}
		receipt = workflow.ReleaseReceipt{ID: release.ID, Number: release.Number}
	}
	for _, effect := range in.Effects {
		if err := (jobQueue{}).EnqueueIn(ctx, tx, jobs.Request{Operation: workflowCompletionJob, Key: fmt.Sprintf("%d/%s", in.RunID, effect), Values: map[string]any{"runId": in.RunID, "effect": effect.String()}}); err != nil {
			return receipt, err
		}
	}
	return receipt, nil
}

// Work supplies its locked completed-Run transaction. Job selection retains
// its existing row locks and retries use that same native transaction.
func (w workflowCompletionEffects) RetryCompletionIn(ctx context.Context, tx *database.Tx, runID ids.ID) (bool, error) {
	out, err := jobs.RetryRunOperationsIn(ctx, tx.Native(), jobs.RunOperations{Operation: workflowCompletionJob, RunID: runID})
	return out.Pending, err
}
