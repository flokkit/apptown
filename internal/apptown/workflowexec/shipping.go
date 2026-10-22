package workflowexec

import (
	"context"
	"log"
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
	// releaseData copies and witnesses a deployment's data around a
	// landing's release; nil takes no copy and witnesses nothing.
	releaseData ReleaseData
	builds      *ReleaseBuilds
	shots       Screenshotter
	cfg         ShippingConfig
	clock       func() time.Time // AppTown's clock; nil is the real one
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
	// ReleaseData copies a deployment's data before a landing releases
	// onto it and witnesses it afterwards (dataprotection.BeforeRelease).
	ReleaseData ReleaseData
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

// ReleaseData is data protection's part in a landing's release: a copy
// before it, and a witness of each need's data after it.
type ReleaseData interface {
	BeforeRelease(ctx context.Context, projectID ids.ID, deployment string, synthetic bool) (ReleaseWitness, error)
}

// ReleaseWitness is what was read before the release: whether the copy
// holds all the data, in words, and the check made once it has booted
// (problems fail the step; notes are what was not compared).
type ReleaseWitness interface {
	Complete() bool
	CopyWords() string
	After(ctx context.Context, declared []string) (problems, notes []string)
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
	return &Shipping{db: db, deployment: d.Deployment, previews: d.Previews, docker: d.Docker, checks: d.Checks, progress: d.Progress, backups: d.Backups, releaseData: d.ReleaseData, builds: d.Builds, shots: d.Screenshots, cfg: cfg, clock: d.Now}
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
