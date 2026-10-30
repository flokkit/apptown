package httpapi

import (
	"github.com/flokkit/apptown/internal/apptown/status"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/workflow"
)

type runRowView struct {
	RunID     ids.ID `json:"runId"`
	RunNumber int64  `json:"runNumber"`
	ChangeID  ids.ID `json:"changeId"`
	// ChangeNumber is 0 when the run has no change.
	ChangeNumber int64                  `json:"changeNumber"`
	Workflow     string                 `json:"workflow"`
	Title        string                 `json:"title"`
	Status       workflow.RunState      `json:"status"`
	Stage        string                 `json:"stage"`
	StatusLabel  status.DisplayStatus   `json:"statusLabel"`
	State        workflow.ActivityState `json:"state,omitempty"`
	Detail       string                 `json:"detail,omitempty"`
	Project      string                 `json:"project,omitempty"`
	StartedAt    string                 `json:"startedAt"`
	DurationS    int                    `json:"durationS"`
	// Next is what the reader can do to the row's change from the list: close
	// a blocked one, when they may.
	Next []workflow.NextAct `json:"next"`
}

func durationSeconds(created, updated string) int {
	t0, e0 := time.Parse(time.RFC3339, created)
	t1, e1 := time.Parse(time.RFC3339, updated)
	if e0 != nil || e1 != nil {
		return 0
	}
	return int(t1.Sub(t0).Seconds())
}

// The overview has different wire field names from the API list, but receives
// the same checked Work summary rather than performing its own SQL lookup.
// A running run ends at now, Work's clock.
func workflowRunRows(rows []workflow.RunSummary, now time.Time) []runRowView {
	out := make([]runRowView, 0, len(rows))
	current := now.UTC().Format(time.RFC3339)
	for _, row := range rows {
		end := row.UpdatedAt
		if row.Status == workflow.RunRunning {
			end = current
		}
		out = append(out, runRowView{RunID: row.ID, RunNumber: row.Number, ChangeID: row.ChangeID, ChangeNumber: row.ChangeNumber, Workflow: row.WorkflowName, Title: row.ItemTitle,
			StatusLabel: row.State.Display(), Status: row.Status, Stage: row.CurrentStage, State: row.State, Detail: row.StateDetail, Project: row.ProjectSlug,
			StartedAt: row.CreatedAt, DurationS: durationSeconds(row.CreatedAt, end), Next: []workflow.NextAct{}})
	}
	return out
}
