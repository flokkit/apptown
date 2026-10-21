package agentenv

import (
	"context"
	"errors"
	"flokkit.io/forge/internal/dockerlabel"
	"time"

	"flokkit.io/forge/internal/execution"
)

// Execute applies Forge cache mounts, labels and host routing to an explicit
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

// WithCacheScope says work under ctx is the named project's: its steps and
// checks share that project's cache volume and no other project's.
func WithCacheScope(ctx context.Context, slug string) context.Context {
	return context.WithValue(ctx, cacheScopeKey{}, slug)
}

// CacheVolume is the cache volume for work under ctx: the project's own
// (apptown-cache-<slug>), or "" when ctx names no project, which mounts none.
func CacheVolume(ctx context.Context) string {
	if slug, _ := ctx.Value(cacheScopeKey{}).(string); slug != "" {
		return ProjectCache(slug)
	}
	return ""
}

// ProjectCache is a project's cache volume. One project's agents and checks
// write their package caches there; another project's never see it.
func ProjectCache(slug string) string { return "apptown-cache-" + slug }

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
