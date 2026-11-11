package dataprotection

import (
	"context"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/ids"
)

// backupRelease records the serving release only while application writers
// run and the deployment is reconciled. A stopped app, an unobserved runtime,
// a failed or in-flight replacement (whose writers may run), or an unknown
// serving release records none. Restoring data never infers a predecessor
// from release numbers.
func (s *Service) backupRelease(ctx context.Context, dep *deployment.Deployment, dk docker.Scope) (ids.ID, error) {
	if dep == nil || dep.ServingReleaseID.IsZero() || dep.Readiness != deployment.Reconciled {
		return ids.Nil, nil
	}
	target, err := s.targets.ByID(ctx, dep.TargetID)
	if err != nil {
		return ids.Nil, err
	}
	running := false
	switch target.DataAccess() {
	case deployment.DataOnDockerHost:
		names, err := dep.HostNames()
		if err != nil {
			return ids.Nil, err
		}
		running = dk.Running(ctx, names.App())
	case deployment.DataThroughPortainer:
		held, err := s.heldData(ctx, dep, target)
		if err != nil {
			return ids.Nil, err
		}
		if held.layout != nil {
			writers, err := s.stackWriters(ctx, dk, *held.layout)
			if err != nil {
				return ids.Nil, err
			}
			running = len(writers) > 0
		}
	case deployment.DataOnManagedPlatform, deployment.DataOnFlyMachines:
		driver, err := s.providers.Driver(ctx, target, dep)
		if err != nil {
			return ids.Nil, err
		}
		observation, err := driver.Observe(ctx, dep.Placement())
		running = err == nil && observation.Rollout == "active"
	}
	if !running {
		return ids.Nil, nil
	}
	return dep.ServingReleaseID, nil
}
