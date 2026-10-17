package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/jobs/internal/jobsql"
	"flokkit.io/forge/internal/stamp"
)

// Job is one operation on the queue as the jobs page shows it.
type Job struct {
	ID         int64          `json:"id"` // River's job id
	Kind       string         `json:"kind"`
	Key        string         `json:"key"`
	State      string         `json:"state"`
	Attempts   int            `json:"attempts"`
	Error      string         `json:"error,omitempty"`
	Receipt    map[string]any `json:"receipt,omitempty"`
	CreatedAt  stamp.Time     `json:"createdAt"`
	StartedAt  stamp.Time     `json:"startedAt,omitzero"`
	FinishedAt stamp.Time     `json:"finishedAt,omitzero"`
	RunAfter   stamp.Time     `json:"runAfter,omitzero"`
}

// List is the open jobs and the recent ones, newest first, capped.
func List(ctx context.Context, q database.Queryer, limit int) ([]Job, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := jobsql.New(q.SQL()).RecentOperationsMeta(ctx, int32(limit))
	out := make([]Job, 0, len(rows))
	if err != nil {
		return out, err
	}
	for _, r := range rows {
		v := Job{ID: r.ID, Kind: r.Operation, Key: r.Key, State: r.State, Attempts: int(r.Attempt), Error: r.Error,
			CreatedAt: stamp.Time{NullTime: sql.NullTime{Time: r.CreatedAt, Valid: true}}, StartedAt: stamp.Time{NullTime: r.AttemptedAt}, FinishedAt: stamp.Time{NullTime: r.FinalizedAt}, RunAfter: stamp.Time{NullTime: sql.NullTime{Time: r.ScheduledAt, Valid: true}}}
		if err := json.Unmarshal(r.Receipt, &v.Receipt); err != nil {
			return []Job{}, fmt.Errorf("reading job %d output: %w", v.ID, err)
		}
		out = append(out, v)
	}
	return out, nil
}
