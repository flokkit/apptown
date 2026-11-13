package apptown

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/deploy/docker"
)

// EnsureDeployments reconciles recorded previews against the hosts at boot.
// A standing deployment whose container is gone is repaired by its host's
// reader (deployment.HostReaders).
func (a *App) EnsureDeployments(ctx context.Context) {
	go a.ensureDeploymentsOnce(ctx)
	// What records name and was made before the installation's mark gets
	// it, once a boot (deployment.Providers.MarkRecorded).
	if err := infrastructureChecksFor(a).request(ctx); err != nil {
		log.Printf("infrastructure: scheduling checks at startup: %v", err)
	}
	if _, err := a.Jobs().Enqueue(ctx, deployment.MarkInstallationJob, "boot", nil, time.Time{}); err != nil {
		log.Printf("deployments: asking to mark recorded resources: %v", err)
	}
}

// ensureDeploymentsOnce forgets previews whose container is gone or stopped.
// Deployment decides; AppTown says what is running.
func (a *App) ensureDeploymentsOnce(ctx context.Context) {
	a.Deployment.BootReconcile(ctx,
		func(targetID ids.ID, container string) (deployment.ContainerPresence, error) {
			dk := a.docker
			if !targetID.IsZero() {
				scoped, err := a.hosts.DockerScopeFor(ctx, targetID)
				if err != nil {
					return deployment.ContainerGone, err
				}
				dk = scoped
			}
			rt, err := docker.Inspect(ctx, dk, container)
			if errors.Is(err, docker.ErrContainerAbsent) {
				return deployment.ContainerGone, nil
			}
			if err != nil {
				return deployment.ContainerGone, err
			}
			if rt.Running {
				return deployment.ContainerRunning, nil
			}
			return deployment.ContainerStopped, nil
		})
}
