package agentenv

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/dockerlabel"
	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/execution"
)

// Execute applies AppTown cache mounts, labels and host routing to an explicit
// process request. The runner retains bounded cancellation and container cleanup.
// at is AppTown's time the step's container is made (its AppTown.created
// label, which maintenance judges by AppTown's clock). The cache is the
// project's that ctx is scoped to (WithCacheScope), or none.
func Execute(runner execution.Runner, ctx context.Context, backend string, req execution.Request, at time.Time) execution.Outcome {
	if backend == execution.Docker {
		req = Decorate(req, at, CacheVolume(ctx))
	}
	out := runner.Run(ctx, backend, req)
	if out.CleanupErr != nil {
		out.RunErr = errors.Join(out.RunErr, out.CleanupErr)
		out.Output = append(out.Output, []byte("\nCleanup failed: "+out.CleanupErr.Error())...)
	}
	return out
}

type cacheScopeKey struct{}

// WithCacheScope says work under ctx is the identified project's: its steps and
// checks share that project's cache volume and no other project's.
func WithCacheScope(ctx context.Context, projectID ids.ID) context.Context {
	return context.WithValue(ctx, cacheScopeKey{}, projectID)
}

// CacheVolume is the cache volume for work under ctx: the project's own
// (apptown-project-cache-<UUID>), or "" when ctx names no project, which mounts none.
func CacheVolume(ctx context.Context) string {
	if id, _ := ctx.Value(cacheScopeKey{}).(ids.ID); !id.IsZero() {
		return ProjectCache(id)
	}
	return ""
}

const projectCachePrefix = "apptown-project-cache-"

// ProjectCache is a project's cache volume. One project's agents and checks
// write their package caches there; another project's never see it.
func ProjectCache(projectID ids.ID) string {
	if projectID.IsZero() {
		return ""
	}
	return projectCachePrefix + projectID.String()
}

// RemoveProjectCache retires only this project's disposable local package cache.
// Absence is success; an unavailable daemon or a volume still in use is an error.
func RemoveProjectCache(ctx context.Context, dk docker.Scope, projectID ids.ID) error {
	if projectID.IsZero() {
		return fmt.Errorf("retiring a project cache requires its identity")
	}
	return dk.RemoveVolume(ctx, ProjectCache(projectID))
}

// ProjectCaches lists only canonical UUID project caches. Legacy slug caches,
// console caches and unknown names are not owned by this inventory.
func ProjectCaches(ctx context.Context, dk docker.Scope) ([]string, error) {
	volumes, err := dk.List(ctx, "volume", "ls", "--format", "{{.Name}}", "--filter", "name="+projectCachePrefix)
	if err != nil {
		return nil, err
	}
	var owned []string
	for _, volume := range volumes {
		if len(volume) <= len(projectCachePrefix) {
			continue
		}
		id, err := ids.Parse(volume[len(projectCachePrefix):])
		if err == nil && !id.IsZero() && ProjectCache(id) == volume {
			owned = append(owned, volume)
		}
	}
	return owned, nil
}

// Decorate applies the cache volume, labels and host routing to a Docker
// step request, for a caller that runs it as a recorded task. An empty
// volume mounts no cache: the run starts cold and shares nothing.
func Decorate(req execution.Request, at time.Time, volume string) execution.Request {
	if volume != "" {
		req.Mounts = append([]string{volume + ":/apptown-cache"}, req.Mounts...)
		req.Env = append([]string{
			"npm_config_cache=/apptown-cache/npm", "GOMODCACHE=/apptown-cache/gomod",
			"GOCACHE=/apptown-cache/gobuild", "UV_CACHE_DIR=/apptown-cache/uv",
			"BUNDLE_PATH=/apptown-cache/bundle", "TRIVY_CACHE_DIR=/apptown-cache/trivy",
		}, req.Env...)
	}
	req.Labels = append([]string{dockerlabel.Class + "=step", dockerlabel.Owner + "=" + req.Name, dockerlabel.Created + "=" + at.UTC().Format(time.RFC3339)}, req.Labels...)
	req.ExtraHosts = append([]string{"host.docker.internal:host-gateway"}, req.ExtraHosts...)
	return req
}
