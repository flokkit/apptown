package usage

import (
	"context"
	"encoding/json"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/usage/internal/usagesql"
)

// ExportProjectIn reads a project's spend rows whole, for moving the project
// to another forge, where they count once as imported spend. Read cursors
// are keyed by this host's paths and do not move.
func ExportProjectIn(ctx context.Context, q database.Queryer, projectID ids.ID) ([]json.RawMessage, error) {
	return database.JSONRows(usagesql.New(q.SQL()).ExportTokenUsage(ctx, projectID))
}

// Moving keeps the spend rows that move with a project: the project's own
// and those of the runs and chats that move. A row of a run (or its root
// run) or a chat that stays stays with it and is counted.
func Moving(rows []json.RawMessage, stayingRuns []ids.ID, stayingChats []string) ([]json.RawMessage, int, error) {
	runs, chats := map[ids.ID]bool{}, map[string]bool{}
	for _, id := range stayingRuns {
		runs[id] = true
	}
	for _, id := range stayingChats {
		chats[id] = true
	}
	out := make([]json.RawMessage, 0, len(rows))
	stayed := 0
	for _, raw := range rows {
		var row struct {
			RunID     ids.ID `json:"run_id"`
			RootRunID ids.ID `json:"root_run_id"`
			SessionID string `json:"session_id"`
		}
		if err := json.Unmarshal(raw, &row); err != nil {
			return nil, 0, err
		}
		if runs[row.RunID] || runs[row.RootRunID] || chats[row.SessionID] {
			stayed++
			continue
		}
		out = append(out, raw)
	}
	return out, stayed, nil
}
