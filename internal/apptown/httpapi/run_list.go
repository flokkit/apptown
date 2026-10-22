package httpapi

import (
	"net/http"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/workflow"
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

// ExecutingSteps: GET /runs/executing — the steps and the operations (a
// copy, a restore, a take-down) executing now, in every project: what
// restarting AppTown would interrupt. A run waiting on a person is not
// executing, nor is a queued operation (a restart queues it again). Empty
// means nothing is executing; a caller who cannot read every project is
// refused, never answered with a partial list.
func (h Runs) ExecutingSteps(w http.ResponseWriter, r *http.Request) {
	steps, err := h.work.ExecutingSteps(r.Context())
	if err != nil {
		WriteFault(w, err)
		return
	}
	running, err := h.ops.Running(r.Context())
	if err != nil {
		WriteFault(w, err)
		return
	}
	out := ExecutingStepsView{Executing: make([]ExecutingStepView, 0, len(steps)), Operations: make([]ExecutingOperationView, 0, len(running))}
	now := h.work.Now()
	for _, step := range steps {
		row := ExecutingStepView{Project: step.Project, RunID: step.RunID, ChangeID: step.ChangeID, Title: step.Title, StepID: step.StepID, Step: step.Name, Type: step.Type}
		if step.StartedAt.Valid {
			row.StartedAt = step.StartedAt.Time.UTC().Format(time.RFC3339)
			row.ElapsedS = int(now.Sub(step.StartedAt.Time).Seconds())
		}
		out.Executing = append(out.Executing, row)
	}
	for _, op := range running {
		row := ExecutingOperationView{Number: op.Number, Kind: op.Kind, AskedBy: op.AskedBy}
		row.Deployment, _ = op.Input["deployment"].(string)
		if cur := op.Current(); cur != nil {
			row.Step = cur.Name
		}
		if op.StartedAt.Valid {
			row.StartedAt = op.StartedAt.Time.UTC().Format(time.RFC3339)
			row.ElapsedS = int(now.Sub(op.StartedAt.Time).Seconds())
		}
		out.Operations = append(out.Operations, row)
	}
	WriteJSON(w, 200, out)
}

type (
	ExecutingStepsView struct {
		Executing []ExecutingStepView `json:"executing"`
		// Operations are the running operations; absent from an older
		// AppTown, which did not report them.
		Operations []ExecutingOperationView `json:"operations"`
	}
	// ExecutingOperationView is one running operation: its number (apptown
	// wait operation <n>), kind, deployment, current step and how long it
	// has run.
	ExecutingOperationView struct {
		Number     int64  `json:"number"`
		Kind       string `json:"kind"`
		Deployment string `json:"deployment,omitempty"`
		Step       string `json:"step,omitempty"`
		AskedBy    string `json:"askedBy"`
		StartedAt  string `json:"startedAt,omitempty"`
		ElapsedS   int    `json:"elapsedS"`
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
