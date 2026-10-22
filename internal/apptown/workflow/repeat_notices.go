package workflow

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
)

// AppTown re-asks a stage's gate after every restart, since the answer can
// change. When nothing about the run has changed, its answer is the one
// already recorded: the system's repeat of an identical refusal, and the
// recovery notice before it, are not written again. Run 422 gathered one
// recovery and one refusal per restart for three weeks. A person's attempt is
// always recorded: that someone tried is evidence.

func latestRunEvent(ctx context.Context, q database.Queryer, runID ids.ID) (kind string, payload json.RawMessage, err error) {
	rows, err := workflowsql.New(q.SQL()).ReadRunEvents(ctx, workflowsql.ReadRunEventsParams{RunID: runID, NewestFirst: true, EventLimit: 1})
	if err != nil || len(rows) == 0 {
		return "", nil, err
	}
	return rows[0].Kind, json.RawMessage(rows[0].PayloadJson), nil
}

// repeatsRefusal says whether the run's latest event is this same system refusal.
func repeatsRefusal(ctx context.Context, q database.Queryer, runID ids.ID, in advanceBlockedReceipt) (bool, error) {
	if in.By != "system" {
		return false, nil
	}
	kind, payload, err := latestRunEvent(ctx, q, runID)
	if err != nil || kind != "advance.blocked" {
		return false, err
	}
	now, err := json.Marshal(in)
	if err != nil {
		return false, err
	}
	var before advanceBlockedReceipt
	if err := json.Unmarshal(payload, &before); err != nil {
		return false, nil //nolint:nilerr // an unreadable old receipt is not a repeat
	}
	was, err := json.Marshal(before)
	if err != nil {
		return false, err
	}
	return bytes.Equal(now, was), nil
}

// lastRefusedBySystem says whether the run's latest event is a system refusal,
// so a recovery that changes nothing need not be noted before re-asking.
func lastRefusedBySystem(ctx context.Context, q database.Queryer, runID ids.ID) (bool, error) {
	kind, payload, err := latestRunEvent(ctx, q, runID)
	if err != nil || kind != "advance.blocked" {
		return false, err
	}
	var before advanceBlockedReceipt
	if err := json.Unmarshal(payload, &before); err != nil {
		return false, nil //nolint:nilerr // an unreadable old receipt is not a repeat
	}
	return before.By == "system", nil
}
