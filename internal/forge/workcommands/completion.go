package workcommands

import (
	"context"
	"errors"
	"log"

	"flokkit.io/forge/internal/forge/jobs"
	"flokkit.io/forge/internal/forge/workflow"
)

func (a FollowUps) Complete(ctx context.Context, key string, _ map[string]any) (map[string]any, error) {
	task, err := a.Work.FollowUp(engineCaller(ctx), key)
	if err != nil {
		if errors.Is(err, workflow.ErrCompletionTask) {
			return nil, jobs.Permanent(err)
		}
		return nil, err
	}
	if task.Removed {
		return map[string]any{"skipped": "run was removed"}, nil
	}
	run := task.Run
	switch task.Effect {
	case workflow.CompletionLand:
		err = a.Landings.Advance(ctx, run.ID)
	case workflow.CompletionAdopt:
		err = a.Uplifts.Complete(ctx, run.ChangeID)
	case workflow.CompletionCleanup:
		// A preview removal that is not confirmed fails the job, which tries
		// again; the rest of the cleanup is safe to repeat.
		if !task.PreviewChangeID.IsZero() {
			err = a.Resources.Previews.DiscardChangePreview(ctx, task.PreviewChangeID)
		}
		err = errors.Join(err, a.Resources.Run(ctx, run.ID))
		// A landed change takes no more work: its chats end.
		if !run.ChangeID.IsZero() {
			ch, readErr := workflow.ReadChangeIn(ctx, a.DB, run.ChangeID)
			if readErr != nil {
				return nil, readErr
			}
			if ch.State != workflow.ChangeInProgress {
				err = errors.Join(err, a.Chats.EndChangeChats(ctx, run.ProjectID, ch.ID))
			}
		}
	case workflow.CompletionNarrative:
		// Durable invocation; replaceable prose retains its best-effort policy.
		// The narrative is a summary: the next read refreshes it again.
		if err := a.Narratives.Refresh(engineCaller(ctx), run.ChangeID); err != nil {
			log.Printf("change %d: refreshing its narrative: %v", run.ChangeID, err)
		}
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"runId": run.ID, "effect": task.Effect.String()}, nil
}
