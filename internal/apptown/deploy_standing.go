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

// EnsureDeployments reconciles recorded deployments against the docker
// host at boot. Standing deployments are STANDING: after a host
// migration, a docker prune, or first boot on a new machine, the DB
// remembers deployments whose containers do not exist here — rebuild
// each from its immutable admitted deployment inputs, on the same stable
// port: the data directory travels with a host move, the containers do
// not.
func (a *App) EnsureDeployments(ctx context.Context) {
	go a.ensureDeploymentsOnce(ctx)
	// What records name and was made before the installation's mark gets
	// it, once a boot (deployment.Providers.MarkRecorded).
	if err := infrastructureChecksFor(a).Request(ctx); err != nil {
		log.Printf("infrastructure: scheduling checks at startup: %v", err)
	}
	if _, err := a.Jobs().Enqueue(ctx, deployment.MarkInstallationJob, "boot", nil, time.Time{}); err != nil {
		log.Printf("deployments: asking to mark recorded resources: %v", err)
	}
}

// ensureDeploymentsOnce reconciles recorded deployments against the hosts
// at boot: previews whose container is gone are forgotten, standing
// deployments whose container is gone are rebuilt from their recorded
// inputs. Deployment decides; AppTown says what is running and asks for the
// rebuild through the queue.
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
		},
		func(e deployment.ProjectDeployment) error {
			// Admission is unique while queued. The deployment attempt key
			// prevents a later boot request from repeating an admitted rebuild.
			_, err := a.Jobs().Enqueue(ctx, "rebuild", e.ProjectID.String()+"/"+e.Name, map[string]any{"deployment": e.ID, "generation": e.ObservedGeneration}, time.Time{})
			return err
		})
}
