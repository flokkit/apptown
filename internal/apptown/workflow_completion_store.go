package apptown

import (
	"context"
	"fmt"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/jobs"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/database"
)

const workflowCompletionJob = "workflow-completion"

// workflowCompletionEffects records a completed run's release; now is the
// AppTown's clock, which stamps it (nil is the real one).
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
		if err := (jobQueue{}).EnqueueIn(ctx, tx, jobs.Request{Operation: workflowCompletionJob, Key: completionKey(in.RunID, effect), Values: map[string]any{"runId": in.RunID, "effect": effect.String()}}); err != nil {
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

// completionKey names one completion effect's job for a run.
func completionKey(runID ids.ID, effect workflow.CompletionEffect) string {
	return fmt.Sprintf("%d/%s", runID, effect)
}

// evalAdoptionJobs reads a shipped adoption Change's settlement for an
// eval: whether the shipping run's adopt job ended without succeeding,
// with its last error. A job not found is not known, never ended.
type evalAdoptionJobs struct{ db *database.DB }

func (j evalAdoptionJobs) AdoptionEnded(ctx context.Context, landingRunID ids.ID) (bool, string, error) {
	job, err := jobs.ByKey(ctx, j.db, workflowCompletionJob, completionKey(landingRunID, workflow.CompletionAdopt))
	if found, err := database.Lookup(err); err != nil || !found {
		return false, "", err
	}
	return job.Ended(), job.Error, nil
}
