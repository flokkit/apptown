package dataprotection

import (
	"context"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/ids"
)

// backupRelease records a settled release only while application writers run.
// A stopped app, an unobserved runtime, a failed or in-flight replacement,
// or historical missing release evidence records none. Restoring data never
// infers a predecessor from release numbers.
func (s *Service) backupRelease(ctx context.Context, dep *deployment.Deployment, dk docker.Scope) (ids.ID, error) {
	if dep == nil || s.releases == nil || dep.ObservedGeneration == 0 || dep.DesiredGeneration != dep.ObservedGeneration {
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
	r, err := s.releases.LastReleasedForDeployment(ctx, dep.ID)
	if found, err := database.Lookup(err); err != nil {
		return ids.Nil, err
	} else if found && r.Generation == dep.ObservedGeneration {
		return r.ID, nil
	}
	return ids.Nil, nil
}
