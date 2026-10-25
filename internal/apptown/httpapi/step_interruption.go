package httpapi

import (
	"context"
	"net/http"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/deploy/docker"
)

type StepInterruption struct {
	work   *workflow.Service
	docker *docker.Scope
	tasks  StepTasks
}

// StepTasks stops a step attempt's recorded tasks, confirmed by their
// backend (workflowexec.TaskRunner).
type StepTasks interface {
	StopStep(ctx context.Context, stepID ids.ID, attempt int64) (stopped []string, found bool, err error)
}

func NewStepInterruption(work *workflow.Service, docker *docker.Scope, tasks StepTasks) StepInterruption {
	return StepInterruption{work: work, docker: docker, tasks: tasks}
}

// StepInterrupt: POST /steps/{id}/interrupt — stop a running agent
// or review step through the product. A step whose attempt runs as tasks
// (on a worker) has their stop recorded and confirmed by the backend;
// otherwise the step's container is removed. Its runner sees the process
// end and records the outcome; the person
// then re-runs it (run-step), under another model if they changed the
// slot. Before this the only way to stop a wandering reviewer was
// docker rm -f on the host (complaint #71).
func (h StepInterruption) StepInterrupt(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r, "id")
	if err != nil {
		WriteError(w, 400, "bad id")
		return
	}
	stopped, err := h.work.InterruptStep(r.Context(), id, func(ctx context.Context, step *workflow.Step) ([]string, error) {
		stopped, found, err := h.tasks.StopStep(ctx, step.ID, step.Attempt)
		if err != nil || found {
			return stopped, err
		}
		return h.docker.StopStep(ctx, step.ID)
	})
	if err != nil {
		WriteFaultOr(w, err, 502)
		return
	}
	WriteJSON(w, 200, StepInterrupted{OK: true, Stopped: stopped})
}

// StepInterrupted names the containers an interrupt stopped.
type StepInterrupted struct {
	OK      bool     `json:"ok"`
	Stopped []string `json:"stopped"`
}
