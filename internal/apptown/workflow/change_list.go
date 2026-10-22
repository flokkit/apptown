package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/stamp"
)

// A change list is one projection shared by HTTP, CLI board and MCP. Summary
// requests omit potentially large bodies at the database boundary.
type ChangeList struct {
	State   ChangeState
	Limit   int
	Summary bool
}

type ChangeSummary struct {
	Change
	Stage        string           `json:"stage"`
	RunStatus    OptionalRunState `json:"runStatus"`
	WorkflowName string           `json:"workflowName"`
	UpdatedAt    stamp.Time       `json:"-"`
}

func readChangeList(ctx context.Context, db database.Queryer, projectID ids.ID, opts ChangeList) ([]ChangeSummary, error) {
	if projectID.IsZero() || opts.Limit < 0 || opts.Limit > math.MaxInt32 {
		return nil, fmt.Errorf("invalid Change list bounds")
	}
	state := ""
	if opts.State != 0 {
		_, err := opts.State.Value()
		if err != nil {
			return nil, err
		}
		state = opts.State.String()
	}
	rows, err := workflowsql.New(db.SQL()).ChangeSummaries(ctx, workflowsql.ChangeSummariesParams{ProjectID: projectID, State: state, LimitCount: int32(opts.Limit), Summary: opts.Summary})
	if err != nil {
		return nil, err
	}
	out := make([]ChangeSummary, 0, len(rows))
	for _, row := range rows {
		changeState, err := ParseChangeState(row.State)
		if err != nil {
			return nil, err
		}
		var runState OptionalRunState
		if err := runState.Scan(row.RunStatus); err != nil {
			return nil, err
		}
		out = append(out, ChangeSummary{Change: Change{ID: row.ID, Number: row.Number, ProjectID: row.ProjectID, Type: row.Type, Title: row.Title, Body: row.Body, RunID: row.RunID, CreatedAt: row.CreatedAt.Format(time.RFC3339Nano), State: changeState, Branch: row.Branch, BaseSHA: row.BaseSha, ExternalRef: row.ExternalRef, Summary: row.Summary, ClosedAt: stamp.Time{NullTime: row.ClosedAt}, ClosedAs: row.ClosedAs}, Stage: row.Stage, RunStatus: runState, WorkflowName: row.WorkflowName, UpdatedAt: stamp.Time{NullTime: sql.NullTime{Time: row.UpdatedAt, Valid: true}}})
	}
	return out, nil
}

// OptionalRunState represents an absent associated run without adding a fourth
// lifecycle state. Its empty-string wire representation preserves existing clients.
type OptionalRunState struct{ State RunState }

func (s OptionalRunState) String() string {
	if s.State == 0 {
		return ""
	}
	return s.State.String()
}
func (s OptionalRunState) MarshalJSON() ([]byte, error) {
	if s.State == 0 {
		return []byte(`""`), nil
	}
	return json.Marshal(s.State)
}
func (s *OptionalRunState) Scan(v any) error {
	if v == "" {
		s.State = 0
		return nil
	}
	return s.State.Scan(v)
}
