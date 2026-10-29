package deployment

import (
	"context"
	"sync"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/jobs"
	"github.com/flokkit/apptown/internal/apptown/operations"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/ids"
)

// Service assembles deployment records, checked user commands and trusted
// execution operations with their configured drivers and collaborators. Its
// exposed record owners are not independently authenticated user entry points.
type Service struct {
	// healthParts is the public part read at each release revision the
	// health controller has asked about; a revision's contract never changes.
	healthMu    sync.Mutex
	healthParts map[string]*appcontract.Part
	// limitsPruned is when the limit events were last pruned; the sweep
	// prunes at most hourly.
	limitsMu     sync.Mutex
	limitsPruned time.Time
	// capacity keeps each target's reading briefly (capacity.go).
	capacity  capacityCache
	hosts     *Hosts
	env       *Environment
	providers *Providers
	projects  *project.Projects
	authority access.Authorizer
	groups    GroupDirectory
	db        *database.DB
	// records is the context a release's log lines are written on: each
	// says what already happened, so none is lost to its caller stopping.
	records            context.Context
	applied            func()
	release            ReleaseDependencies
	configuration      ConfigurationDependencies
	observationTimeout time.Duration
	observations       *observations
	diagnosed          diagnosed           // the last part facts a diagnostics read saw (diagnostics.go)
	ops                *operations.Service // the ledger, read for a take-down holding a deployment
	starter            CopyStarter         // data protection's fill of a deployment started from a copy
	filler             RestoreFiller       // data protection's fill of a rebuild after AppTown was restored
	copyCheck          DataCopyCheck       // whether AppTown can copy what a deployment holds now
	// now is AppTown's clock: every time a rule stores and later judges
	// (observations' age among them) is written and read by it, so a test
	// can set it.
	clock func() time.Time // nil is the real clock

	Targets     *Targets
	Deployments *Deployments
	Releases    *Releases
	Outcomes    *Outcomes
	Bundles     *Bundles
	Retained    *Retained
	Needs       NeedStore
	Inventory   *Inventory
}

// Dependencies is what Deployment asks of the world. Every field is an
// explicit owner, setting or port; AppTown supplies them at construction,
// and a standalone tool supplies its own values.
//
// Targets and Deployments are the record owners the shared
// collaborators write too, and Hosts, Environment and Providers those
// collaborators: the composition root builds each once and gives the same
// instance to every owner that uses it. Nil ones are an AppTown without
// Docker, providers or registered needs (a standalone tool or a test):
// New builds empty ones over the same records.
type Dependencies struct {
	Targets       *Targets
	Deployments   *Deployments
	Hosts         *Hosts
	Environment   *Environment
	Providers     *Providers
	Projects      *project.Projects
	Authority     access.Authorizer
	Groups        GroupDirectory
	Releases      ReleaseDependencies
	Configuration ConfigurationDependencies
	Followups     jobs.Enqueuer // settlement's after-commit work
	Declared      Declarations  // the app's declaration, for what a target holds
	Applied       func()        // called after a deployment is applied; nil is fine
	// Operations is the ledger data protection's operations run through,
	// which a release reads for a take-down holding its deployment; nil:
	// none runs.
	Operations *operations.Service
	// CopyStarter is data protection's side of starting a deployment from a
	// copy; nil refuses every start from a copy.
	CopyStarter CopyStarter
	// RestoreFiller is data protection's fill of a built-in deployment's
	// rebuild after AppTown was restored (restore_check.go).
	RestoreFiller RestoreFiller
	// CopyCheck says whether AppTown can copy the data a deployment holds
	// now, which a release asks before it is admitted; nil asks nothing.
	CopyCheck DataCopyCheck
	// Now is AppTown's clock; nil is the real one.
	Now func() time.Time
}

func New(ctx context.Context, db *database.DB, deps Dependencies) *Service {
	targets := deps.Targets
	if targets == nil {
		targets = NewTargets(db, nil)
	}
	deployments := deps.Deployments
	if deployments == nil {
		deployments = NewDeployments(db)
	}
	hosts := deps.Hosts
	if hosts == nil {
		hosts = NewHosts(targets, nil, HostSettings{Scope: docker.Scope{}})
	}
	env := deps.Environment
	if env == nil {
		// The project record reads the same database; without one the
		// environment could not say what any project is for.
		projects := deps.Projects
		if projects == nil {
			projects = project.New(db, nil)
		}
		env = NewEnvironment(EnvironmentDependencies{Projects: projects})
	}
	providers := deps.Providers
	if providers == nil {
		providers = NewProviders(ctx, db, ProviderRecords{Targets: targets, Deployments: deployments}, hosts, env, ProviderDependencies{Projects: deps.Projects, Now: deps.Now})
	}
	s := &Service{
		records:            context.WithoutCancel(ctx),
		hosts:              hosts,
		env:                env,
		providers:          providers,
		projects:           deps.Projects,
		authority:          deps.Authority,
		groups:             deps.Groups,
		db:                 db,
		release:            deps.Releases,
		configuration:      deps.Configuration,
		observationTimeout: 5 * time.Second,
		observations:       &observations{entries: make(map[ids.ID]observationEntry)},
		applied:            deps.Applied,
		Targets:            targets,
		Deployments:        deployments,
		Releases:           NewReleases(db),
		Outcomes:           NewOutcomes(db, deps.Followups),
		Bundles:            NewBundles(db),
		Retained:           NewRetained(db),
		ops:                deps.Operations,
		starter:            deps.CopyStarter,
		filler:             deps.RestoreFiller,
		copyCheck:          deps.CopyCheck,
		Needs:              NewNeedStore(db, nil),
		Inventory:          NewInventory(db, deps.Declared),
	}
	s.Inventory.targets, s.Inventory.providers = targets, providers
	s.setClock(deps.Now)
	return s
}

// now reads AppTown's clock.
func (s *Service) now() time.Time { return clock.Or(s.clock)() }

// setClock puts the owner and every part that stamps or judges a time on
// now, AppTown's clock (the real clock when nil). A bundle's and a
// release's times are compared with when Changes closed (release
// membership), so they, and everything else here, are AppTown's clock.
func (s *Service) setClock(now func() time.Time) {
	s.clock = now
	s.Releases.clock, s.Bundles.clock, s.Outcomes.clock, s.Targets.clock = now, now, now, now
	s.Deployments.clock, s.Needs.clock = now, now
	s.Inventory.deployments.clock, s.Inventory.needs.clock = now, now
}
