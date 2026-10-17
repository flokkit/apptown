package forge

import (
	"context"
	"os/exec"

	"flokkit.io/forge/internal/execution"
)

// Executors: where a step's process runs. The abstraction is the executor;
// sandboxing is a property (local has none, docker does). The full contract is
// stage-in → run → stage-out; local and local-docker collapse staging via
// cwd/bind-mount — Prepare/Collect materialize when a remote backend lands
// (see design/architecture/architecture.md §5.3).

const (
	ExecutorLocal  = "local"
	ExecutorDocker = "docker"
)

type ExecRequest = execution.Request

type ExecOutcome = execution.Outcome

// DockerAvailable pings the daemon once; used for a friendly startup warning.
// A forge with no local Docker has none to ping.
func (a *App) DockerAvailable(ctx context.Context) bool {
	if a.docker.Absent() {
		return false
	}
	if _, err := exec.LookPath("docker"); err != nil {
		return false
	}
	return a.docker.OK(ctx, "info", "--format", "ok")
}
