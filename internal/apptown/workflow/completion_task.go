package workflow

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/flokkit/apptown/internal/ids"
)

var ErrCompletionTask = errors.New("invalid completion task")

// completionTask is a current observation for one already-admitted follow-up.
// It grants no replay of an external effect and holds no SQL connection.
type completionTask struct {
	Run             *Run
	Effect          CompletionEffect
	Removed         bool
	PreviewChangeID ids.ID
}

func parseCompletionEffect(value string) (CompletionEffect, error) {
	for _, effect := range []CompletionEffect{CompletionCleanup, CompletionAdopt, CompletionLand, completionParent} {
		if effect.String() == value {
			return effect, nil
		}
	}
	return 0, fmt.Errorf("%w: unknown completion effect %q", ErrCompletionTask, value)
}

// FollowUp validates a completed run's durable task and handles Work-owned parent
// advancement. The caller composes the remaining effects with their domain owners.
func (s *Service) FollowUp(ctx context.Context, key string) (completionTask, error) {
	var out completionTask
	if _, err := s.require(ctx, operationPermission{Action: actionIdentify}); err != nil {
		return out, err
	}
	idText, effect, ok := strings.Cut(key, "/")
	if !ok {
		return out, fmt.Errorf("%w: invalid completion job key %q", ErrCompletionTask, key)
	}
	id, err := ids.Parse(idText)
	if err != nil {
		return out, fmt.Errorf("%w: %w", ErrCompletionTask, err)
	}
	selected, err := parseCompletionEffect(effect)
	if err != nil {
		return out, err
	}
	run, err := readRun(ctx, s.db, id)
	if errors.Is(err, sql.ErrNoRows) {
		return completionTask{Removed: true}, nil
	}
	if err != nil {
		return out, err
	}
	if _, err := s.require(ctx, operationPermission{Action: actionAdvanceRun, ProjectID: run.ProjectID, RunID: run.ID}); err != nil {
		return out, err
	}
	current, err := readScopedRun(ctx, s.db, run.ID, run.ProjectID)
	if errors.Is(err, sql.ErrNoRows) {
		return out, ErrRunMoved
	}
	if err != nil {
		return out, err
	}
	if *current != *run {
		return out, ErrRunMoved
	}
	if run.Status != RunDone {
		return out, fmt.Errorf("%w: run %d is %s, not completed", ErrCompletionTask, id, run.Status)
	}
	out = completionTask{Run: run, Effect: selected}
	switch selected {
	case completionParent:
		parent, err := (advanceStore{db: s.db}).completedParent(ctx, run)
		if err != nil {
			return completionTask{}, err
		}
		if parent != nil {
			if _, err := s.AdvanceFrom(ctx, parent); err != nil {
				return completionTask{}, err
			}
		}
	case CompletionCleanup:
		spec, err := run.Definition()
		if err != nil {
			return completionTask{}, err
		}
		if run.ParentRunID.IsZero() && !run.ChangeID.IsZero() && spec.Role != "working" {
			out.PreviewChangeID = run.ChangeID
		}
	}
	return out, nil
}
