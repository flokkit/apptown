package workflowexec

import (
	"time"

	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/forge/dependency"
	"flokkit.io/forge/internal/forge/repositoryedit"
	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/progress"
)

const ScriptKind = "script"
const TestsKind = "tests"

// Commands executes script and test steps. It shares Workflow's execution
// contract with agent work but requires no model, identity or harness service.
type Commands struct {
	db        *database.DB
	cfg       CommandConfig
	runner    execution.Runner
	progress  *progress.Feeds
	publisher repositoryedit.Publisher
	branches  *scm.Branches
	tasks     *TaskRunner
	panics    interface{ Panic(string, any, []byte) }
	docker    *docker.Scope
	checks    CompileCheck
	catalog   *dependency.Catalog
	clock     func() time.Time // AppTown's clock; nil is the real one
}

// now reads AppTown's clock.
func (a *Commands) now() time.Time { return clock.Or(a.clock)() }

type CommandConfig struct {
	DataDir, Image string
	// HostScripts lets a script step that asks for `executor: local` run as
	// a process on this host: the operator's explicit choice (-host-scripts),
	// for an AppTown whose projects are trusted as much as its host.
	HostScripts bool
}
type CommandDependencies struct {
	Runner    execution.Runner
	Progress  *progress.Feeds
	Publisher repositoryedit.Publisher
	Branches  *scm.Branches
	// Tasks runs Docker script steps as recorded tasks. Nil keeps the
	// unrecorded path (a runner that dies with AppTown).
	Tasks  *TaskRunner
	Panics interface{ Panic(string, any, []byte) }
	// Docker is asked which containers still write a script's folder.
	Docker *docker.Scope
	// Checks runs a tests step's command, on AppTown's check executor:
	// the worker when one is configured, else this machine. Its zero value
	// runs them through Runner, here.
	Checks CompileCheck
	// Catalog fulfils the app's declared needs (.apptown/app.yaml) as the
	// services a tests step's command runs beside.
	Catalog *dependency.Catalog
	// Now is AppTown's clock; nil is the real one.
	Now func() time.Time
}

func NewCommands(db *database.DB, deps CommandDependencies, cfg CommandConfig) *Commands {
	c := &Commands{db: db, cfg: cfg, runner: deps.Runner, progress: deps.Progress, publisher: deps.Publisher, branches: deps.Branches, tasks: deps.Tasks, panics: deps.Panics, docker: deps.Docker, checks: deps.Checks, catalog: deps.Catalog, clock: deps.Now}
	if deps.Tasks != nil {
		deps.Tasks.commands = c
	}
	return c
}
