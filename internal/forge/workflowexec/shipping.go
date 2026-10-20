package workflowexec

import (
	"context"
	"log"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/forge/deployment"
	"flokkit.io/forge/internal/forge/preview"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/verification"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/progress"
)

const DeployKind = "deploy"
const PreviewKind = "preview"

// NotContinued are the step kinds an AppTown restart ends instead of
// continuing: a deploy step builds, publishes and deploys, and a restart
// leaves it in error until someone runs it again (design/operations/workers.md,
// "Releases built on the worker"). Its release-build task, if any, is stopped and released
// (tasks.UnsettledStepsIn), and its deployment attempt is observed by the
// deployment owner, never applied again by a resumed step.
func NotContinued() []string { return []string{DeployKind} }

// Shipping adapts deployment effects into workflow evidence and settlement.
// Deployment still owns release admission, generation guards and providers.
type Shipping struct {
	db         *database.DB
	deployment *deployment.Service
	previews   *preview.Service
	docker     *docker.Scope
	checks     CompileCheck
	progress   *progress.Feeds
	backups    interface{ AfterRelease(ids.ID, string) }
	builds     *ReleaseBuilds
	shots      Screenshotter
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
	Backups  interface{ AfterRelease(ids.ID, string) }
	// Builds, on an AppTown with a worker, is where its releases are built;
	// nil builds each on its target (docker.PrepareImages), as before.
	Builds *ReleaseBuilds
	// Now is AppTown's clock, which stamps what shipping records on a
	// run; nil is the real one.
	Now func() time.Time
	// Screenshots pictures a run's preview once its boot passes
	// (verification.Screenshots); nil takes none, and the boot's evidence
	// says so.
	Screenshots Screenshotter
}

// Screenshotter takes AppTown's pictures of a preview's pages.
type Screenshotter interface {
	Take(ctx context.Context, base string, pages []string) verification.Capture
}

// CompileCheck runs one check container and answers with its outcome
// (verification.CheckRun), in the image the executor's machine has.
type CompileCheck struct {
	Run   func(ctx context.Context, req execution.Request) execution.Outcome
	Image string
}

func NewShipping(db *database.DB, d ShippingDependencies, cfg ShippingConfig) *Shipping {
	return &Shipping{db: db, deployment: d.Deployment, previews: d.Previews, docker: d.Docker, checks: d.Checks, progress: d.Progress, backups: d.Backups, builds: d.Builds, shots: d.Screenshots, cfg: cfg, clock: d.Now}
}
func (a *Shipping) projectSlugOf(ctx context.Context, id ids.ID) string {
	p, _ := project.ByIDIn(ctx, a.db, id)
	return p.Slug
}
func recordArtifact(ctx context.Context, db *database.DB, runID ids.ID, kind, name, ref, body string, at time.Time) {
	ctx = context.WithoutCancel(ctx) // a record of what happened
	if _, err := workflow.AddArtifactIn(ctx, db, runID, kind, name, ref, body, at); err != nil {
		log.Printf("background write failed: %v — artifact %s %q on run %d", err, kind, name, runID)
	}
}

// now reads AppTown's clock.
func (a *Shipping) now() time.Time { return clock.Or(a.clock)() }
