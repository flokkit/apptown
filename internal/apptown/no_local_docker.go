package apptown

import (
	"context"
	"log"
	"time"

	piharness "github.com/flokkit/apptown/internal/apptown/agentharness/pi"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/execution"
)

// projectProcesses runs project processes on this host (agents, scripts,
// checks, drafts): AppTown's runner, refused on an AppTown with no local
// Docker. AppTown's own Git keeps processRunner.
func projectProcesses(a *App) execution.Runner {
	r := a.processRunner
	if a.cfg.noLocalDocker {
		r.Refused = docker.ErrNoLocalBackend
	}
	return r
}

// workerAgentLabels reads the agent image's labels from the worker, for a
// AppTown with no local Docker to read them from. A worker that does not
// answer gives no labels, so nothing is promised that cannot be checked.
// life is the App's: the labels are read when a home is prepared, by
// callers with no context to give.
func workerAgentLabels(life context.Context, w *execution.WorkerTasks) func() piharness.Labels {
	return func() piharness.Labels {
		ctx, cancel := context.WithTimeout(life, 10*time.Second)
		defer cancel()
		image, err := w.AgentImage(ctx)
		if err != nil {
			log.Printf("reading the worker's agent image labels: %v", err)
		}
		return piharness.MapLabels(image.Labels)
	}
}
