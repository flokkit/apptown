package httpapi

import (
	"net/http"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/forge/workflow"
)

func (h Runs) ListRuns(w http.ResponseWriter, r *http.Request) {
	query := workflow.RunQuery{ProjectSlug: r.URL.Query().Get("project"), State: workflow.RunRunning, Limit: 100}
	if status := r.URL.Query().Get("status"); status == "all" {
		query.State = 0
	} else if status != "" {
		var err error
		query.State, err = workflow.ParseRunState(status)
		if err != nil {
			WriteError(w, 400, "%v", err)
			return
		}
	}
	rows, err := h.work.Runs(r.Context(), query)
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, rows)
}

// ExecutingSteps: GET /runs/executing — the steps executing now, in every
// project: what restarting the forge would interrupt. A run waiting on a
// person is not executing. Empty means nothing is executing; a caller who
// cannot read every project is refused, never answered with a partial list.
func (h Runs) ExecutingSteps(w http.ResponseWriter, r *http.Request) {
	steps, err := h.work.ExecutingSteps(r.Context())
	if err != nil {
		WriteFault(w, err)
		return
	}
	out := ExecutingStepsView{Executing: make([]ExecutingStepView, 0, len(steps))}
	now := h.work.Now()
	for _, step := range steps {
		row := ExecutingStepView{Project: step.Project, RunID: step.RunID, ChangeID: step.ChangeID, Title: step.Title, StepID: step.StepID, Step: step.Name, Type: step.Type}
		if step.StartedAt.Valid {
			row.StartedAt = step.StartedAt.Time.UTC().Format(time.RFC3339)
			row.ElapsedS = int(now.Sub(step.StartedAt.Time).Seconds())
		}
		out.Executing = append(out.Executing, row)
	}
	WriteJSON(w, 200, out)
}

type (
	ExecutingStepsView struct {
		Executing []ExecutingStepView `json:"executing"`
	}
	// ExecutingStepView is one executing step and how long it has run.
	ExecutingStepView struct {
		Project   string `json:"project"`
		RunID     ids.ID `json:"runId"`
		ChangeID  ids.ID `json:"changeId"`
		Title     string `json:"title"`
		StepID    ids.ID `json:"stepId"`
		Step      string `json:"step"`
		Type      string `json:"type"`
		StartedAt string `json:"startedAt,omitempty"`
		ElapsedS  int    `json:"elapsedS"`
	}
)
