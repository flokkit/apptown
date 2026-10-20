package verification

import (
	"context"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/forge/preview"

	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/forge/adoption"
	"flokkit.io/forge/internal/forge/conformance"
	"flokkit.io/forge/internal/forge/deployment"
	"flokkit.io/forge/internal/forge/model"
	"flokkit.io/forge/internal/forge/usage"
)

const GuidanceKind = "guidance/conformance"

type Guidance struct {
	db           *database.DB
	dataDir      string
	adoption     *adoption.Service
	conformance  *conformance.Service
	scanners     *Scanners
	evaluator    *Evaluator
	verification *Changes
	runtime      *Runtime
	docker       *docker.Scope
	previews     PreviewOpener
	routing      deployment.Routing
	selection    model.Selection
	ledger       *usage.Ledger
	clock        func() time.Time
}

// PreviewOpener opens a change's or a run's running preview for probing,
// wherever it runs (deployment.Service).
type PreviewOpener interface {
	OpenPreview(ctx context.Context, projectID ids.ID, label string) (*preview.Served, bool, error)
}
type GuidanceDependencies struct {
	DB          *database.DB
	DataDir     string
	Adoption    *adoption.Service
	Conformance *conformance.Service
	Scanners    *Scanners
	Evaluator   *Evaluator
	Changes     *Changes
	Runtime     *Runtime
	Docker      *docker.Scope
	Previews    PreviewOpener
	Routing     deployment.Routing
	Selection   model.Selection
	Ledger      *usage.Ledger
	Now         func() time.Time // AppTown's clock; nil is the real one
}

// now is AppTown's clock.
func (a *Guidance) now() time.Time { return clock.Or(a.clock)() }

func NewGuidance(d GuidanceDependencies) *Guidance {
	return &Guidance{db: d.DB, dataDir: d.DataDir, adoption: d.Adoption, conformance: d.Conformance, scanners: d.Scanners, evaluator: d.Evaluator, verification: d.Changes, runtime: d.Runtime, docker: d.Docker, previews: d.Previews, routing: d.Routing, selection: d.Selection, ledger: d.Ledger, clock: d.Now}
}

type ChangeDependencies struct {
	DB          *database.DB
	Adoption    *adoption.Service
	Conformance *conformance.Service
	Scanners    *Scanners
	Evaluator   *Evaluator
	DataDir     string
	// WorkerChats saves a chat on the worker that holds a change before the
	// change is checked; nil checks every change as it stands.
	WorkerChats WorkerChats
	Now         func() time.Time // AppTown's clock; nil is the real one
}

func NewChanges(d ChangeDependencies) *Changes {
	return &Changes{db: d.DB, adoption: d.Adoption, conformance: d.Conformance, scanners: d.Scanners, evaluator: d.Evaluator, dataDir: d.DataDir, workerChats: d.WorkerChats, baseline: &baselineCache{entries: make(map[string]*baselineSet)}, clock: d.Now}
}

// now reads AppTown's clock.
func (v *Changes) now() time.Time { return clock.Or(v.clock)() }
