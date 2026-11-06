package workflowexec

import (
	"context"
	"fmt"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/preview"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/verification"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/progress"
)

const BuildKind = "build"
const PreviewKind = "preview"

// NotContinued are the step kinds an AppTown restart ends instead of
// continuing: a build step builds and publishes, and a restart
// leaves it in error until someone runs it again (design/operations/workers.md,
// "Releases built on the worker"). Its release-build task, if any, is stopped and released
// (tasks.UnsettledStepsIn). Deployment release requests have their own
// execution and recovery.
func NotContinued() []string { return []string{BuildKind} }

// shipping adapts builds and previews into workflow evidence and settlement.
// Deployment still owns release admission, generation guards and providers.
type shipping struct {
	db         *database.DB
	deployment *deployment.Service
	previews   *preview.Service
	docker     *docker.Scope
	checks     CompileCheck
	progress   *progress.Feeds
	builds     *ReleaseBuilds
	shots      screenshotter
	cfg        ShippingConfig
	clock      func() time.Time // AppTown's clock; nil is the real one
}
type ShippingConfig struct {
	DataDir, Image string
	Routing        deployment.Routing
}
type ShippingDependencies struct {
	Deployment *deployment.Service
	// Previews boots a run's preview step.
	Previews *preview.Service
	Docker   *docker.Scope
	// Checks runs the landing's compile check where checks run: the
	// worker's executor when one is configured.
	Checks   CompileCheck
	Progress *progress.Feeds
	// Builds, on an AppTown with a worker, is where its releases are built;
	// nil builds each on its target (docker.PrepareImages), as before.
	Builds *ReleaseBuilds
	// Now is AppTown's clock, which stamps what shipping records on a
	// run; nil is the real one.
	Now func() time.Time
	// Screenshots pictures a run's preview once its boot passes
	// (verification.Screenshots); nil takes none, and the boot's evidence
	// says so.
	Screenshots screenshotter
}

// screenshotter takes AppTown's pictures of a preview's pages.
type screenshotter interface {
	Take(ctx context.Context, base string, pages []string) verification.Capture
}

// CompileCheck runs one check container and answers with its outcome
// (verification.CheckRun), in the image the executor's machine has.
type CompileCheck struct {
	Run   func(ctx context.Context, req execution.Request) execution.Outcome
	Image string
}

func NewShipping(db *database.DB, d ShippingDependencies, cfg ShippingConfig) *shipping {
	return &shipping{db: db, deployment: d.Deployment, previews: d.Previews, docker: d.Docker, checks: d.Checks, progress: d.Progress, builds: d.Builds, shots: d.Screenshots, cfg: cfg, clock: d.Now}
}
func (a *shipping) projectSlugOf(ctx context.Context, id ids.ID) string {
	p, _ := project.ByIDIn(ctx, a.db, id)
	return p.Slug
}

// recordArtifact writes what a step made (a build, a preview, a merge
// request): the step's evidence of what is running. The effect has happened,
// so it is written though the caller stops; one that cannot be written is
// the step's error, never a pass.
func recordArtifact(ctx context.Context, db database.Queryer, runID ids.ID, kind, name, ref, body string, at time.Time) error {
	if _, err := workflow.AddArtifactIn(context.WithoutCancel(ctx), db, runID, kind, name, ref, body, at); err != nil {
		return fmt.Errorf("recording the %s %q: %w", kind, name, err)
	}
	return nil
}

// now reads AppTown's clock.
func (a *shipping) now() time.Time { return clock.Or(a.clock)() }
