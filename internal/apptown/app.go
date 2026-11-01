package apptown

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"

	"github.com/flokkit/apptown/internal/oidclogin"

	"github.com/flokkit/apptown/internal/apptown/instanceupdate"
	"github.com/flokkit/apptown/internal/version"

	"github.com/flokkit/apptown/internal/apptown/dataprotection"
	"github.com/flokkit/apptown/internal/apptown/instancebackup"
	"github.com/flokkit/apptown/internal/apptown/preview"
	"github.com/flokkit/apptown/internal/toolversions"

	"github.com/flokkit/apptown/internal/apptown/tasks"

	"github.com/flokkit/apptown/internal/apptown/inference"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/adrs"
	"github.com/flokkit/apptown/internal/apptown/agentenv"
	"github.com/flokkit/apptown/internal/apptown/attention"
	"github.com/flokkit/apptown/internal/apptown/verification"
	"github.com/flokkit/apptown/internal/apptown/workflowexec"
	"github.com/flokkit/apptown/internal/console"
	"github.com/flokkit/apptown/internal/console/pi"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/worker"

	piharness "github.com/flokkit/apptown/internal/apptown/agentharness/pi"
	"github.com/flokkit/apptown/internal/apptown/changeactions"
	"github.com/flokkit/apptown/internal/apptown/changeconsole"
	"github.com/flokkit/apptown/internal/apptown/complaint"
	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/conversation"
	"github.com/flokkit/apptown/internal/apptown/dependency"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/evals"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/jobs"
	"github.com/flokkit/apptown/internal/apptown/maintenance"
	"github.com/flokkit/apptown/internal/apptown/model"
	"github.com/flokkit/apptown/internal/apptown/operations"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/projectlifecycle"
	"github.com/flokkit/apptown/internal/apptown/remediation"
	"github.com/flokkit/apptown/internal/apptown/repositoryedit"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/apptown/usage"
	"github.com/flokkit/apptown/internal/apptown/workcommands"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/progress"
)

// App is the composition root and the remaining application orchestration.
// Provider dependencies are assembled in deployment_wiring.go; drivers do not
// receive this object. workflow_wiring.go supplies the advancement engine's
// operations. Database schema and migrations belong to dbschema.
type App struct {
	// images is the runtime images of the tool versions this AppTown shipped with.
	images toolversions.Images
	// newWork holds new jobs and steps for a whole-AppTown backup's brief
	// pause (instancebackup).
	newWork instancebackup.Hold
	// Backups takes whole-AppTown backups and records what a restored
	// AppTown does on its first start.
	Backups *instancebackup.Service
	// life is the App's lifetime: background work runs under it, and
	// ShutdownBackground ends it.
	life             context.Context
	endLife          context.CancelFunc
	preparation      *workcommands.Preparation
	agents           *workflowexec.Executor
	workshop         *agentenv.Workshop
	consoleAdmission *conversation.Admission
	changeConsole    *changeconsole.Service
	narratives       narratives
	cfg              configuration
	setup            setupState
	credentials      *credentialLimiter // the doors that take a secret (internal_auth.go, door)
	Access           *identity.Service
	InstanceUpdates  *instanceupdate.Service
	Adoption         *adoption.Service
	Ways             *wow.Service

	repositoryAttachments *scm.RepositoryAttachments
	repositories          scm.RepositoryLocks
	branches              *scm.Branches // the only writer of change branches; shares repositories' locks
	tasks                 *workflowexec.TaskRunner
	// dumps is where AppTown's database dumps run (databaseDump).
	dumps         dumpPlace
	conformance   *conformance.Service
	evaluator     verification.Effects
	scanners      *verification.Scanners
	scanSteps     *verification.ScanSteps
	guidance      *verification.Guidance
	steering      *agentenv.Instructions
	briefing      *conversation.Briefing
	probe         *docker.Probe
	health        *docker.HealthController
	docker        docker.Scope
	processRunner execution.Runner // its project processes go through projectProcesses
	runTok        identity.RuntimeTokens
	DB            *database.DB
	Deployment    *deployment.Service // architecture.md §5.6; built once in New
	Preview       *preview.Service    // previews of changes, runs and releases; built once in New
	// DataProtection holds copies, restores, restored copies and take-downs; built once in New.
	DataProtection *dataprotection.Service
	// Deployment's shared records and collaborators (deployment.Dependencies).
	targets             *deployment.Targets
	deployments         *deployment.Deployments
	hosts               *deployment.Hosts
	environment         *deployment.Environment
	targetProviders     *deployment.Providers
	operations          *operations.Service        // the ledger of long operations; kinds register at construction
	deletion            *projectlifecycle.Deletion // a project's deletion, kept period and purge; built once (it holds chats' blocks)
	Projects            *project.Projects
	Integrations        *integration.Service
	Secrets             *secret.Service
	Usage               *usage.Service
	Models              *model.Slots
	modelSelection      model.Selection
	Complaints          *complaint.Box
	Dependencies        *dependency.Catalog
	ADRs                *adrs.Service
	Work                *workflow.Service
	attachments         workflow.Attachments
	standingRules       workflow.StandingRules
	routinePublications wow.RoutineTransitions
	changeActions       *changeactions.Service
	repositoryEdits     *repositoryedit.Service
	Fixes               *remediation.Service // the remediation facade, built once in New
	// The provider inventory cache (infrastructure_cache.go).
	inventory          *deployment.InventoryReadings
	jobs               *jobs.Queue
	reaper             *maintenance.Reaper
	disks              maintenance.Disks
	diskObserver       *maintenance.DiskObserver
	attention          *attention.Service
	workerObserver     *maintenance.WorkerObserver
	capacityObserver   *deployment.CapacityObserver
	protectionObserver *dataprotection.ProtectionObserver
	infrastructure     *infrastructureChecks
	// The committed-tree command and shared conformance baseline cache.
	verification *verification.Changes
	capacity     *model.Scheduler
	// AppTown's own error reporting (self_errors.go).
	selfErr selfErrors
	// AppTown's own traces, metrics and logs (telemetry_wiring.go).
	telemetry *instrumentation
	// Live progress feeds for running agent steps (live.go).
	progress progress.Feeds
	// Host routing observations, separate from immutable tier configuration.
	vhostMu    sync.Mutex
	vhostCache map[string]vhostEntry
	// vhostRules decides which projects a tier domain serves (vhost.go).
	vhostRules *vhostRules
	// workerPreviews boots and forwards changes' and runs' previews on the
	// worker; nil keeps them on this machine.
	workerPreviews *worker.Previews
	// Console sessions (console.go) — in-memory; dirs under _data/console.
	Console        *console.Service
	consoleRuntime *pi.Runtime
	// workerChats runs chats placed on the worker (nil without a worker):
	// console.Placed routes each chat to it or to consoleRuntime.
	workerChats *worker.RemoteAgent
	// chatSaves saves a worker chat before a landing, recheck, check or
	// preview chooses its work; changeConsole is built over it.
	chatSaves       *changeconsole.Saves
	consoleStop     chan struct{} // closed by StopConsoles: the rounds and rehearsal loops end
	consoleStopOnce sync.Once
	lifecycle       *projectlifecycle.Service
	inference       *inference.Gateway
	providers       *inference.Catalog
	cli             interface{ Path() (string, error) }
	evalGate        *evals.Gate
	// Evals drives evals: copies, arms, scorecards.
	Evals *evals.Service
	// The user MCP endpoint (mcp_server.go), built once so sessions outlive
	// the route table.
	mcpDoor http.Handler
	// The HTTP surface, built on Handler's first call and served from then
	// on: its routes hold the owners the App has when it is built.
	routes routeTable
}

// RunHealthController runs until daemon cancellation.
func (a *App) RunHealthController(ctx context.Context) { a.health.Run(ctx) }

// New constructs application state only; startup repair is explicit. ctx is
// the App's lifetime: its background work runs under it until
// ShutdownBackground ends it.
func New(ctx context.Context, db *database.DB, input Config) (*App, error) {
	cfg, err := input.prepare()
	if err != nil {
		return nil, err
	}
	if db == nil {
		return nil, fmt.Errorf("constructing App requires a database")
	}
	if cfg.stock.FS == nil || cfg.stock.Tools == nil || cfg.stock.Name == "" {
		return nil, fmt.Errorf("no stock: AppTown reads its ways of working and tool versions from the update files its release shipped with")
	}
	// The start snapshot: what is release-bound (the private Docker image)
	// is read from it once; the rest follows the
	// owner, one snapshot per unit of work.
	images := cfg.stock.Tools.Current().Images
	cfg.proxies.hosts = hostAddressSnapshot(input.HostAddresses)
	client := input.DockerClient
	if client == nil {
		client = docker.CLI{}
	}
	if cfg.noLocalDocker {
		client = docker.Refusing{Attached: client}
	}
	// The installation's id marks what it makes at targets; the inventory
	// counts only what carries it. Construction only reads it (the daemon's
	// data layout step writes it first): without one, nothing reads as this
	// installation's.
	installation, err := existingInstanceID(cfg.dataDir)
	if err != nil {
		return nil, fmt.Errorf("reading the installation id: %w", err)
	}
	a := &App{cfg: cfg, images: images, docker: docker.Scope{Client: client, Now: cfg.clock.Now, Instance: installation}, probe: input.DockerProbe, DB: db,
		processRunner: input.ProcessRunner,
		consoleStop:   make(chan struct{}),
		cli:           agentenv.NewCLI(cfg.consoleCLI),
		inventory:     deployment.NewInventoryReadings(db, cfg.clock.Now),
		credentials:   newCredentialLimiter(cfg.clock.Now)}
	a.life, a.endLife = context.WithCancel(ctx)
	// Generated files (registry configuration) are written in AppTown's
	// own folder, beside each run's tree, never in it.
	a.processRunner.Limits = cfg.taskLimits
	if a.processRunner.Scratch == "" {
		a.processRunner.Scratch = filepath.Join(cfg.dataDir, "generated")
	}
	a.branches = scm.NewBranches(db, &a.repositories)
	a.branches.Now = cfg.clock.Now
	// The local profile runs tasks on AppTown's own Docker with its default
	// network and no resource limits imposed (zero is none, and is recorded).
	// A configured worker replaces it: tasks run on the worker's own Docker,
	// with its default network, and no script runs on this host.
	// Each attempt records the backend it was admitted to, by identity: this
	// installation's containers on the Docker daemon selected at startup (by
	// its address, which every call is pinned to), or the worker at its
	// address. A restarted AppTown configured for another
	// backend leaves such attempts alone.
	namespace := taskNamespace(cfg.dataDir)
	daemon := execution.SelectDocker()
	records := tasks.New(db)
	records.Now = cfg.clock.Now
	local := execution.DockerTasks{Namespace: namespace, Daemon: daemon, Files: filepath.Join(cfg.dataDir, "task-files"), Now: cfg.clock.Now, ToolVersions: cfg.stock.Tools}
	// AppTown's database is dumped where AppTown runs its containers: its
	// own Docker when it has one (the built-in database is only there),
	// otherwise its worker.
	a.dumps = dumpPlace{tasks: local, scratch: filepath.Join(cfg.dataDir, "dump-tasks"), profile: "local-docker", now: cfg.clock.Now}
	if cfg.noLocalDocker {
		a.dumps = dumpPlace{tasks: *cfg.worker, results: *cfg.worker, profile: "worker", now: cfg.clock.Now}
	}
	a.tasks = &workflowexec.TaskRunner{Records: records, Driver: local,
		Requests: filepath.Join(cfg.dataDir, "tasks"),
		Profile:  workflowexec.TaskProfile{Name: "local-docker", Limits: cfg.taskLimits, Network: execution.NetworkDefault},
		Backend:  "local-docker " + namespace + " on " + daemon.Backend(), Now: cfg.clock.Now}
	if cfg.worker != nil {
		a.tasks.Driver, a.tasks.Remote = *cfg.worker, *cfg.worker
		a.tasks.Profile.Name, a.tasks.Backend = "worker", maintenance.WorkerBackend+cfg.worker.URL
		// Changes' and runs' previews boot on the worker, and nowhere else,
		// under this start's session: greater than every earlier start's.
		session, err := worker.NextSession(filepath.Join(cfg.dataDir, "worker-session"), cfg.clock.Now)
		if err != nil {
			return nil, fmt.Errorf("previews on the worker need this start's session: %w", err)
		}
		a.workerPreviews = &worker.Previews{URL: cfg.worker.URL, Client: cfg.worker.Client, Session: strconv.FormatInt(session, 10)}
	}
	if a.telemetry, err = startInstrumentation(ctx, cfg); err != nil {
		return nil, err
	}
	a.progress.Now = cfg.clock.Now
	setupToken := ""
	if cfg.authMode == "internal" {
		setupToken = filepath.Join(cfg.dataDir, "setup-token")
	}
	a.Access = identity.New(db, identity.Config{OpenInstance: cfg.authMode == "open", Projects: project.NewCatalogue(db), Now: cfg.clock.Now, SetupTokenPath: setupToken, External: &identity.ExternalDependencies{Internal: cfg.authMode == "internal", Origin: cfg.publicOrigin(), Protocol: oidclogin.New(nil, cfg.clock.Now, cfg.publicHostScheme == "http"), Secret: func(ctx context.Context, name string) (string, error) {
		values, err := a.Secrets.WithContext(ctx)
		return values[name], err
	}}})
	a.InstanceUpdates, err = instanceupdate.New(instanceupdate.Options{DataDir: cfg.dataDir, Build: version.String(), Platform: runtime.GOOS + "/" + runtime.GOARCH, Channel: input.UpdateChannel, Authority: access.Checked{Authority: a.Access}, Now: cfg.clock.Now})
	if err != nil {
		return nil, err
	}
	a.Projects = project.NewWithClock(db, identity.Scoped(a.Access, "project"), cfg.clock.Now)
	a.vhostRules = &vhostRules{db: db, projects: a.Projects, now: cfg.clock.Now}
	a.Secrets = secret.NewService(db, secret.Config{DataDir: cfg.dataDir, Client: input.SecretsHTTPClient, Now: cfg.clock.Now})
	a.repositoryAttachments = scm.NewRepositoryAttachments(db, scm.AttachmentDependencies{Projects: a.Projects, Authority: access.Checked{Authority: identity.Scoped(a.Access, "scm"), Projects: a.Projects}, Provider: repositoryProvider{bindings: integration.NewWithClock(db, cfg.clock.Now), secrets: a.Secrets}, Locks: &a.repositories, DataDir: cfg.dataDir, Now: cfg.clock.Now})
	a.Dependencies = dependency.NewCatalog(db, cfg.clock.Now)
	a.Integrations = integration.NewService(db, integration.Dependencies{Kinds: a.Dependencies, Projects: a.Projects, Secrets: a.Secrets, SCMRepositories: scmRepositoryTokens{a.repositoryAttachments}, Now: cfg.clock.Now})
	a.workshop = agentenv.NewWorkshop(&a.docker, a.Integrations, &a.progress, cfg.dataDir, cfg.agentImage, images.PrivateDocker, cfg.clock.Now)
	a.workshop.Limits = cfg.taskLimits
	a.Usage = usage.NewService(db, access.Checked{Authority: identity.Scoped(a.Access, "usage"), Projects: a.Projects}, cfg.clock.Now)
	// The evals' one rule, asked at every effect for a copy: each step's
	// claim, each merge, each model dispatch (evals.Gate).
	a.evalGate = evals.NewGate(db, a.Usage.Ledger)
	a.Models = model.New(db)
	a.Models.Now = cfg.clock.Now
	a.modelSelection = model.NewSelection(a.Models)
	// Each model provider states its own steps at once (Integrations); one
	// AppTown cannot read runs a step at a time.
	a.capacity = model.NewScheduler(model.BindingCapacity{Bindings: a.Integrations.Bindings}, 1)
	a.Complaints = complaint.NewBox(db, complaintPackVersions{db}, cfg.clock.Now)
	a.Ways = wow.New(db, wow.Dependencies{Stock: cfg.stock.FS, Shipped: cfg.stock.Name, ToolLines: cfg.stock.Tools, Importers: waysImporters{db}, Checks: waysChecks{}, Now: cfg.clock.Now})
	changes := workflow.NewChanges(db, access.Checked{Authority: identity.Scoped(a.Access, "work"), Projects: a.Projects}, cfg.clock.Now)
	a.routinePublications = wow.NewRoutineTransitions(db, access.Checked{Authority: identity.Scoped(a.Access, "routine-publications")}, a.Access, cfg.clock.Now)
	a.Adoption = adoption.New(db, adoption.Dependencies{RoutinePublications: a.routinePublications, Authority: access.Checked{Authority: identity.Scoped(a.Access, "adoption"), Projects: a.Projects}, Ways: a.Ways, Capabilities: adoptionCapabilities{}, Uplift: workcommands.Uplift{Changes: changes, DB: db, Lanes: func() remediation.WorkflowResolver { return a.lifecycle }, Runs: func() workcommands.RunStarter { return a.Work }}, Reviews: adoptionReviews{fleet: func() *verification.Fleet { return fleetReviewsFor(a) }}, Copies: project.Copies{}, Now: cfg.clock.Now})
	var extensionNames []string
	for _, ext := range piharness.Extensions() {
		extensionNames = append(extensionNames, ext.Name)
	}
	a.lifecycle = projectlifecycle.New(db, projectlifecycle.Dependencies{Jobs: jobQueue{}, Identity: a.Access, Adoption: a.Adoption, Projects: a.Projects, Ways: a.Ways, Repositories: &a.repositories, Publisher: repositoryPublisher{attachments: a.repositoryAttachments, db: db, now: cfg.clock.Now}, Changes: changes, Superseded: workflow.NewAbandonments(db, abandonmentEffects{now: cfg.clock.Now}, &a.repositories).WithClock(cfg.clock.Now), Secrets: a.Secrets}, projectlifecycle.Config{DataDir: cfg.dataDir, Extensions: extensionNames, Seeding: cfg.seeding, Now: cfg.clock.Now})
	a.conformance = conformance.New(db, access.Checked{Authority: identity.Scoped(a.Access, "conformance"), Projects: a.Projects}, verification.NewSource(a.Adoption, cfg.dataDir), cfg.clock.Now)
	// Work consumes remediation's stateless budget participant. Remediation
	// then consumes the fully constructed Work service.
	a.steering = agentenv.NewInstructions(db, a.Integrations)
	a.providers = inference.NewCatalog(db, a.Secrets)
	a.providers.Now = cfg.clock.Now
	consoleDeps := a.consoleDependencies(input.ConsoleAgent, input.ConsoleWorker)
	a.Console = console.New(consoleDeps)
	// The gateway is always built: AppTown's own model calls go through
	// it (Ask). -inference-url opens its listener to runtimes.
	a.inference = inference.New(cfg.inferenceNetworks, inferenceIdentityFor(a), a.providers, a.Usage.Requests).Clocked(cfg.clock.Now).Instrument(a.telemetry.tracer, a.telemetry.meter).LinkAttempts(a.telemetry.links.Lookup).
		ChooseModels(a.evalGate.AdmitModel).ChooseJudge(a.evalGate.AdmitJudge)
	// Built before its users; the change console is built over it.
	a.chatSaves = changeconsole.NewSaves(changeconsole.SavesDependencies{DB: db, Sessions: a.Console, Remote: a.workerChats, Branches: a.branches,
		Publisher: repositoryPublisher{attachments: a.repositoryAttachments, db: db, now: cfg.clock.Now}, DataDir: cfg.dataDir, Now: cfg.clock.Now})
	a.operations = operations.New(db, access.Checked{Authority: identity.Scoped(a.Access, "operations"), Projects: a.Projects}, a.Access, cfg.clock.Now)
	// Deployment's records the shared collaborators write, and the
	// collaborators themselves, are built once here and shared with every
	// owner that uses them.
	a.targets = deployment.NewTargets(db, targetConnectors{}, deployment.IntranetTargetName)
	a.deployments = deployment.NewDeployments(db)
	a.hosts = deployment.NewHosts(a.targets, a.Secrets, hostSettingsFor(a))
	a.environment = deployment.NewEnvironment(environmentDependenciesFor(a))
	a.infrastructure = &infrastructureChecks{}
	a.targetProviders = deployment.NewProviders(ctx, db, deployment.ProviderRecords{Targets: a.targets, Deployments: a.deployments}, a.hosts, a.environment, providerDependenciesFor(a, input.DigitalOcean))
	// Data protection's fill of a deployment started from a copy is built
	// before Deployment, which is given it; data protection itself is
	// built over the same one after.
	starter := dataprotection.NewStarter(db, starterDependenciesFor(a))
	deploymentDeps := a.deploymentDependencies(changes)
	deploymentDeps.Operations = a.operations
	deploymentDeps.CopyStarter = starter
	deploymentDeps.CopyCheck = starter
	deploymentDeps.RestoreFiller = starter
	a.Deployment = deployment.New(ctx, db, deploymentDeps)
	a.vhostRules.deployments = a.Deployment.Deployments // standing hostnames route by its records
	a.DataProtection = dataprotection.New(starter, dataProtectionDependencies(a))
	a.Backups = instancebackup.New(instanceBackupDependencies(a))
	a.Preview = preview.New(db, previewDependencies(a, changes))
	a.deletion = projectDeletionFor(a)
	a.agents = agentExecutionFor(a, agentenv.NewModelProcess(projectProcesses(a), a.providers, cfg.inferenceURL != "", cfg.clock.Now))
	// Construct runner owners before binding their method values into Work.
	a.evaluator = verification.NewEffects(checksFor(a).Run, runtimeFor(a), a.inference)
	a.scanners = scannersFor(a)
	a.scanSteps = scanStepsFor(a)
	a.verification = changesFor(a)
	a.guidance = guidanceFor(a)
	a.preparation = workcommands.NewPreparation(db, a.Adoption, a.branches, cfg.dataDir, holderPresenceFor(a), a.chatSaves, a.Secrets, cfg.clock.Now)
	a.Work = workflow.New(a.life, db, a.workDependencies())
	a.attachments = workflow.NewAttachments(db, access.Checked{Authority: identity.Scoped(a.Access, "work"), Projects: a.Projects}, workflow.GitWorkspace{DataDir: cfg.dataDir}, cfg.clock.Now)
	a.standingRules = workflow.NewStandingRules(db, workflow.StandingDependencies{Adoptions: a.Adoption, Inspector: verification.NewRoutineInspector(cfg.dataDir, &a.repositories, checksFor(a)), Presence: holderPresenceFor(a), Authority: access.Checked{Authority: identity.Scoped(a.Access, "standing-approval"), Projects: a.Projects}, Identity: a.Access, Procedures: workcommands.StandingProcedures{Lanes: a.lifecycle, Laws: workflowLawRules{}}, Destinations: standingDestinations{}, Now: cfg.clock.Now})
	a.ADRs = adrs.New(db, access.Checked{Authority: identity.Scoped(a.Access, "adrs"), Projects: a.Projects}, refreshADREvidence(a.Work, a.asEngine(a.life)), cfg.clock.Now)
	a.Fixes = remediation.New(db, a.remediationDependencies())
	a.Evals = evals.New(db, evalsDependencies(a))
	a.briefing = conversation.NewBriefing(a.Ways, a.Adoption, conversation.BriefingConfig{DataDir: cfg.dataDir, PublicHost: cfg.publicHost, FrontDeskVerbs: consoleAllowedVerbs(false), WorkshopVerbs: consoleAllowedVerbs(true)})
	a.changeActions = changeactions.New(a.Work, db)
	a.repositoryEdits = repositoryedit.New(a.Work, a.Dependencies, repositoryPublisher{attachments: a.repositoryAttachments, db: db, now: cfg.clock.Now}, a.branches, a.lifecycle, db, cfg.dataDir)
	a.narratives = narratives{builder: narrativeFor(a), db: db, engine: a.asEngine}
	a.changeConsole = changeConsoleFor(a)
	a.consoleAdmission = consoleAdmissionFor(a)
	a.health = a.Deployment.HealthController()
	a.reaper = maintenance.New(db, maintenance.Dependencies{Docker: a.docker, Sessions: a.Console, Runtime: consoleDeps.Agent, ToolVersions: cfg.stock.Tools, Now: cfg.clock.Now}, cfg.dataDir)
	a.disks = maintenance.Disks{DataDir: cfg.dataDir, Now: cfg.clock.Now}
	if cfg.worker != nil {
		a.disks.Worker = *cfg.worker
	}
	var workerProbe maintenance.WorkerProbe
	workerIdentity := ""
	if cfg.worker != nil {
		workerProbe = *cfg.worker
		workerIdentity = fmt.Sprintf("%x", sha256.Sum256([]byte(cfg.worker.URL)))
	}
	a.disks.WorkerIdentity = workerIdentity
	a.diskObserver = maintenance.NewDiskObserver(db, a.disks, cfg.clock.Now)
	a.workerObserver = maintenance.NewWorkerObserver(db, workerIdentity, workerProbe, workerObservationRequests{db: db}, cfg.clock.Now)
	a.capacityObserver = deployment.NewCapacityObserver(a.Deployment)
	a.protectionObserver = dataprotection.NewProtectionObserver(a.DataProtection, protectionObservationRequests{db: db})
	*a.infrastructure = infrastructureChecksFor(a)
	a.jobs, err = jobs.New(jobs.Options{Tracer: a.telemetry.tracer, Hold: a.newWork.Wait}, db.Pool(), apptownJobKinds(a), jobReporter{}, apptownJobSchedules()...)
	if err != nil {
		return nil, fmt.Errorf("constructing job queue: %w", err)
	}
	a.attention = attentionFor(a)
	a.mcpDoor = newMCPDoor(a)
	return a, nil
}

// taskNamespace names this installation's task containers on a Docker daemon
// it may share with another AppTown: derived from the data directory, so it
// needs no file written at construction and is the same after a restart. A
// data directory moved while tasks run leaves those tasks unrecognised; they
// are then reported as unknown, never adopted by the wrong AppTown.
func taskNamespace(dataDir string) string {
	if abs, err := filepath.Abs(dataDir); err == nil {
		dataDir = abs
	}
	sum := sha256.Sum256([]byte(dataDir))
	return hex.EncodeToString(sum[:6])
}
