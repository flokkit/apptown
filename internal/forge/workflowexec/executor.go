// Package workflowexec orchestrates workflow step execution and agent drafting. Workflow owns claims
// and settlement; this application facade orders preparation and external effects.
package workflowexec

import (
	"context"
	"log"
	"runtime/debug"
	"time"

	"flokkit.io/forge/internal/ids"

	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/forge/adoption"
	"flokkit.io/forge/internal/forge/agentenv"
	piharness "flokkit.io/forge/internal/forge/agentharness/pi"
	"flokkit.io/forge/internal/forge/dependency"
	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/forge/integration"
	"flokkit.io/forge/internal/forge/model"
	"flokkit.io/forge/internal/forge/repositoryedit"
	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/forge/secret"
	"flokkit.io/forge/internal/forge/usage"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/progress"
)

const AgentKind = "agent/pi"
const ReviewKind = "agent/review"

type Process interface {
	Run(context.Context, string, execution.Request) execution.Outcome
}
type Config struct {
	DataDir, Image, Backend, APIURL string
	NoPrivateDocker                 bool // apptownd -no-private-docker: a step declaring it is refused
	// PrivateDockerOnWorker is apptownd -private-docker: a step declaring
	// private Docker is admitted on a worker, qualified or not by the
	// worker's measured profile. Without it, it is refused there.
	PrivateDockerOnWorker bool
}
type Dependencies struct {
	Adoption     *adoption.Service
	Integrations *integration.Service
	Secrets      *secret.Service
	Catalog      *dependency.Catalog
	Models       model.Selection
	Capacity     *model.Scheduler
	Progress     *progress.Feeds
	Tokens       *identity.RuntimeTokens
	Docker       *docker.Scope
	Workshop     *agentenv.Workshop
	Instructions *agentenv.Instructions
	Home         piharness.Preparer
	CLI          interface{ Path() (string, error) }
	Process      Process
	Ledger       *usage.Ledger
	Publisher    repositoryedit.Publisher
	Branches     *scm.Branches
	Panics       interface{ Panic(string, any, []byte) }
	// Worker runs agent and review steps when set, and nowhere else
	// (agent_attempt.go, review_worker.go). Nil runs both on AppTown's own
	// Docker.
	Worker *AgentWorker
	// Telemetry; nil means no-op.
	Tracer trace.Tracer
	Meter  metric.Meter
	Links  AttemptLinks
	// Now is AppTown's clock, which stamps the run records this executor
	// writes (artifacts); nil is the real one.
	Now func() time.Time
}
type Executor struct {
	db             *database.DB
	cfg            Config
	adoption       *adoption.Service
	integrations   *integration.Service
	secrets        *secret.Service
	dependencies   *dependency.Catalog
	modelSelection model.Selection
	capacity       *model.Scheduler
	progress       *progress.Feeds
	runTok         *identity.RuntimeTokens
	docker         *docker.Scope
	workshop       *agentenv.Workshop
	obs            observer
	steering       *agentenv.Instructions
	home           piharness.Preparer
	cli            interface{ Path() (string, error) }
	process        Process
	ledger         *usage.Ledger
	publisher      repositoryedit.Publisher
	branches       *scm.Branches
	panics         interface{ Panic(string, any, []byte) }
	worker         *AgentWorker
	// changedFiles reads a change's files for the review scope guard. It is
	// repositoryedit.ChangedFilesSince; the review tests replace it because
	// their changes have no repository to fail against.
	changedFiles func(ctx context.Context, ws, base string) ([]repositoryedit.ChangedFile, error)

	// Settle bounds how an agent's or reviewer's finished outcome is
	// recorded again when its write fails (settleProduced). Its zero value
	// is the default bound.
	Settle workflow.Settlement

	clock func() time.Time // AppTown's clock; nil is the real one
}

// now reads AppTown's clock.
func (a *Executor) now() time.Time { return clock.Or(a.clock)() }

func New(db *database.DB, deps Dependencies, cfg Config) *Executor {
	return &Executor{db: db, cfg: cfg, adoption: deps.Adoption, integrations: deps.Integrations, secrets: deps.Secrets, dependencies: deps.Catalog, modelSelection: deps.Models, capacity: deps.Capacity, progress: deps.Progress, runTok: deps.Tokens, docker: deps.Docker, workshop: deps.Workshop, steering: deps.Instructions, home: deps.Home, cli: deps.CLI, process: deps.Process, ledger: deps.Ledger, publisher: deps.Publisher, branches: deps.Branches, panics: deps.Panics, worker: deps.Worker, changedFiles: repositoryedit.ChangedFilesSince, obs: newObserver(deps.Tracer, deps.Meter, deps.Links, deps.Now), clock: deps.Now}
}
func recordEvent(ctx context.Context, db *database.DB, runID ids.ID, kind string, payload any, at time.Time) {
	ctx = context.WithoutCancel(ctx) // a record of what happened
	workflow.RecordNoticeOrLog(ctx, db, runID, kind, payload, at)
}
func reportPanic(reporter interface{ Panic(string, any, []byte) }, where string) {
	if r := recover(); r != nil {
		stack := debug.Stack()
		log.Printf("PANIC in %s: %v\n%s", where, r, stack)
		if reporter != nil {
			reporter.Panic(where, r, stack)
		}
	}
}
