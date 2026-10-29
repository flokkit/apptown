package workcommands

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/jobs"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/workflow"
)

func (a FollowUps) Abandon(ctx context.Context, key string, args map[string]any) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	parts := strings.Split(key, "/")
	if len(parts) != 3 {
		return nil, jobs.Permanent(fmt.Errorf("invalid abandonment job key %q", key))
	}
	id, err := ids.Parse(parts[1])
	if err != nil {
		return nil, jobs.Permanent(err)
	}
	switch {
	case parts[0] == "change" && parts[2] == "cleanup":
		ch, err := workflow.ReadChangeIn(ctx, a.DB, id)
		if errors.Is(err, sql.ErrNoRows) {
			return map[string]any{"skipped": "change was removed"}, nil
		}
		if err != nil {
			return nil, err
		}
		if ch.State != workflow.ChangeAbandoned {
			return map[string]any{"skipped": "change is no longer abandoned"}, nil
		}
		p, projectErr := project.ByIDIn(ctx, a.DB, ch.ProjectID)
		if projectErr != nil {
			return nil, fmt.Errorf("project %d is unavailable", ch.ProjectID)
		}
		// Job arguments are persisted by the same transaction as these run IDs.
		var runIDs []ids.ID
		raw, err := json.Marshal(args["runIds"])
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &runIDs); err != nil {
			return nil, jobs.Permanent(err)
		}
		// Every part is tried, and safe to repeat; a removal that is not
		// confirmed fails the job, which tries again.
		var failed []error
		for _, runID := range runIDs {
			failed = append(failed, a.Resources.Run(ctx, runID))
		}
		failed = append(failed, a.Chats.EndChangeChats(ctx, p.ID, ch.ID), a.Resources.Change(ctx, ch.ID))
		if err := errors.Join(failed...); err != nil {
			return nil, err
		}
	case parts[0] == "run" && parts[2] == "ended":
		run, err := workflow.ReadRunIn(ctx, a.DB, id)
		if errors.Is(err, sql.ErrNoRows) {
			return map[string]any{"skipped": "run was removed"}, nil
		}
		if err != nil {
			return nil, err
		}
		if run.Status != workflow.RunAbandoned {
			return map[string]any{"skipped": "run is no longer abandoned"}, nil
		}
		if err := a.tellLandingEnded(ctx, run); err != nil {
			return nil, err
		}
	default:
		return nil, jobs.Permanent(fmt.Errorf("unknown abandonment effect %q", key))
	}
	return map[string]any{"target": parts[0], "id": id, "effect": parts[2]}, nil
}

// tellLandingEnded tells the chats of a change whose landing ended without
// landing (main moved under it, a person stopped it or sent it back) why,
// in AppTown's words, which say what to do next. The change is back in
// progress and its chat may be the one that handed it over: told nothing,
// change 61's chat went on saying its landing awaited approval. This job
// runs once per ended run (one attempt), so each ending is told once; a
// chat that could not be told fails the job, where the failure is kept.
func (a FollowUps) tellLandingEnded(ctx context.Context, run *workflow.Run) error {
	if !run.ParentRunID.IsZero() || run.ChangeID.IsZero() {
		return nil
	}
	spec, err := run.Definition()
	if err != nil || spec.Role != "landing" {
		return err
	}
	ch, err := workflow.ReadChangeIn(ctx, a.DB, run.ChangeID)
	if err != nil || ch.State != workflow.ChangeInProgress {
		return err
	}
	ended, err := a.Work.Snapshot(engineCaller(ctx), run.ID, workflow.DetailView)
	if err != nil {
		return err
	}
	msg := fmt.Sprintf("[apptown landing] This change's landing (run %d) ended without landing anything.", run.ID)
	if ended.Ending != "" {
		msg = strings.TrimSuffix(msg, ".") + ": " + ended.Ending
	}
	return a.Chats.TellChangeChats(ctx, ch.ProjectID, ch.ID, msg)
}
