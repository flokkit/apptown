package apptown

import (
	"context"
	"os/exec"

	"github.com/flokkit/apptown/internal/execution"
)

type execRequest = execution.Request

type execOutcome = execution.Outcome

// DockerAvailable pings the daemon once; used for a friendly startup warning.
// An AppTown with no local Docker has none to ping.
func (a *App) DockerAvailable(ctx context.Context) bool {
	if a.docker.Absent() {
		return false
	}
	if _, err := exec.LookPath("docker"); err != nil {
		return false
	}
	return a.docker.OK(ctx, "info", "--format", "ok")
}
