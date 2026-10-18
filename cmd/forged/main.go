package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"flokkit.io/forge/internal/version"

	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/forge/integration"
	"flokkit.io/forge/internal/forge/projectlifecycle"

	"flokkit.io/forge/internal/forge"
	"flokkit.io/forge/internal/forge/verification"
	"flokkit.io/forge/stock"
)

// daemonFlags is forged's command line. Every flag can also be set in the
// environment as FORGE_ plus its name in capitals with - as _ (bindEnv); a
// flag on the command line wins.
type daemonFlags struct {
	hostScripts                                      bool
	taskCPUs, chatCPUs                               float64
	taskMemoryMiB, taskPIDs, chatMemoryMiB, chatPIDs int64
	addr                                             string
	data                                             string
	agentExecutor                                    string
	agentImage                                       string
	agentModel                                       string
	dbURL                                            string
	authMode                                         string
	openOnNetwork                                    bool
	authHeader                                       string
	authSecretEnv                                    string
	admins                                           string
	trustedProxies                                   string
	consoleCLI                                       string
	workerURL                                        string
	workerCert                                       string
	workerKey                                        string
	workerCA                                         string
	checkWorker                                      string
	consoleAddr                                      string
	consoleURL                                       string
	inferenceAddr                                    string
	inferenceURL                                     string
	inferenceNetworks                                string
	publicHost                                       string
	restoreGateImage                                 string
	mirrorBase                                       string
	features                                         string
	otlpEndpoint                                     string
	otlpProtocol                                     string
	errorsURL                                        string
	agentConc                                        int
	portainerEnv                                     string
	verifyOnly                                       bool
	noPrivateDocker                                  bool
	noLocalDocker                                    bool
	drainLocalDocker                                 bool
	resetPassword                                    string
	backfillMain                                     bool
	privateDocker                                    bool
	tierDomains                                      []string
	gitImportPrivate                                 bool
	bundleMaxBytes                                   int64
}

func defineFlags(fs *flag.FlagSet) *daemonFlags {
	o := &daemonFlags{}
	fs.StringVar(&o.addr, "addr", ":7777", "listen address")
	fs.StringVar(&o.data, "data", "./_data", "data directory (db, workspaces)")
	fs.IntVar(&o.agentConc, "agent-concurrency", 1, "max concurrent agent/pi steps (match your model backend's stream capacity)")
	fs.StringVar(&o.agentExecutor, "agent-executor", "docker", "default executor for agent/pi steps: docker | local (local is experimental, for development)")
	fs.StringVar(&o.agentImage, "agent-image", "forge-agent", "docker image for containerized steps")
	fs.StringVar(&o.agentModel, "agent-model", "", "default model for agent/pi steps, e.g. gpu1/Qwen3.6 (empty = pi's default)")
	fs.StringVar(&o.portainerEnv, "portainer-environment", integration.DefaultPortainerEnvironment, "the Portainer environment the intranet target releases to, when the Portainer secrets (PORTAINER_URL, PORTAINER_API_KEY, FORGE_APPS_DOMAIN) are set; recorded in the portainer-deploy integration at every start")
	fs.StringVar(&o.dbURL, "db-url", "", "Postgres URL for the forge's own database (env FORGE_DB_URL; empty = the built-in container on this box, design/subsystems/postgres-migration.md)")
	fs.BoolVar(&o.gitImportPrivate, "git-import-allow-private", false, "let a project that starts from an https repository fetch from private, loopback and link-local addresses (an internal GitLab); by default only public addresses are fetched (design/operations/start-from-existing-code.md)")
	fs.Int64Var(&o.bundleMaxBytes, "import-bundle-max-bytes", projectlifecycle.DefaultBundleMaxBytes, "the largest Git bundle a project that starts from existing code may upload")
	fs.BoolVar(&o.privateDocker, "private-docker", false, "admit agent steps that declare private Docker (docker: true) on the worker: the operator's opt-in; every such attempt records whether the worker's measured profile is qualified (design/operations/workers.md)")
	fs.BoolVar(&o.noPrivateDocker, "no-private-docker", false, "refuse agent steps that declare private Docker (docker: true), and give change consoles none")
	fs.BoolVar(&o.noLocalDocker, "no-local-docker", false, "run with no local Docker daemon and no project process on this host: everything runs on the worker (-worker-url), releases go to attached Docker targets, and the database is external (-db-url); a forge that last ran with its own Docker first needs -drain-local-docker (design/operations/workers.md)")
	fs.BoolVar(&o.drainLocalDocker, "drain-local-docker", false, "one-shot, with this forge's normal settings and its own Docker, while it is not serving: repair interrupted work, check that no local work or execution container is left, record the drain that admits a start with -no-local-docker, and exit (non-zero, naming what is left, otherwise)")
	fs.BoolVar(&o.backfillMain, "backfill-recorded-main", false, "one-shot, with this forge's normal settings while no forged serves its database: record each repository-backed project's current main as where recording began (projects already recorded are kept, an unreadable main stays unknown), print the counts, and exit without serving (for projects created before the forge recorded main)")
	fs.StringVar(&o.resetPassword, "reset-password", "", "one-shot break-glass, with this forge's normal settings on its host: give the named existing account a new random password (its sessions end; one receipt names forge-operator (host)), print only that password on stdout, and exit without serving (design/operations/README.md, Locked out of the admin account)")
	fs.BoolVar(&o.verifyOnly, "verify-only", false, "start normally, which upgrades the database and the data directory to this build, then serve reads only: no recovery, no workers, no scheduler (the verification startup after a move or restore; not a dry run of an upgrade)")
	fs.StringVar(&o.authMode, "auth", "open", "auth mode: open | internal | trusted-header (design/subsystems/auth.md)")
	fs.BoolVar(&o.openOnNetwork, "open-on-network", false, "with -auth open, serve on addresses other machines can reach; without it open mode refuses any listener but loopback, since in open mode a caller names itself")
	fs.StringVar(&o.authHeader, "auth-header", "X-Authentik-Username", "trusted-header mode: header carrying the SSO username")
	fs.StringVar(&o.authSecretEnv, "auth-proxy-secret-env", "FORGE_PROXY_SECRET", "env var holding the proxy shared secret (trusted-header mode)")
	fs.StringVar(&o.admins, "admins", "", "comma-separated usernames to seed/promote as admin")
	fs.StringVar(&o.trustedProxies, "trusted-proxies", "", "comma-separated CIDRs of reverse proxies whose X-Real-IP/X-Forwarded-For name the caller (loopback and this host's own addresses are always trusted)")
	fs.StringVar(&o.consoleCLI, "console-cli", "", "Linux forge CLI artifact for containers (default: forge-linux beside forged)")
	fs.BoolVar(&o.hostScripts, "host-scripts", false, "experimental, for development: let a script step with executor: local run as a process on this host; off, a project's script runs only in a container")
	fs.Float64Var(&o.taskCPUs, "task-cpus", execution.DefaultTaskLimits.CPUs, "CPUs for each step, check or task container, here or on the worker")
	fs.Int64Var(&o.taskMemoryMiB, "task-memory-mib", execution.DefaultTaskLimits.MemoryBytes>>20, "memory, swap included, in MiB, for each step, check or task container")
	fs.Int64Var(&o.taskPIDs, "task-pids", execution.DefaultTaskLimits.PIDs, "processes for each step, check or task container")
	fs.Float64Var(&o.chatCPUs, "chat-cpus", execution.DefaultChatLimits.CPUs, "CPUs for each chat's container on this host (a worker has its own -chat-cpus)")
	fs.Int64Var(&o.chatMemoryMiB, "chat-memory-mib", execution.DefaultChatLimits.MemoryBytes>>20, "memory, swap included, in MiB, for each chat's container on this host")
	fs.Int64Var(&o.chatPIDs, "chat-pids", execution.DefaultChatLimits.PIDs, "processes for each chat's container on this host")
	fs.StringVar(&o.consoleAddr, "console-addr", ":7778", "GET-only API listener for console sessions (empty = disabled)")
	fs.StringVar(&o.consoleURL, "console-url", "", "the address chats use to reach the console listener, http(s)://host:port (empty = http://host.docker.internal:<console-addr port>, reachable only from this host's containers; a worker's chats need an address the worker can reach)")
	fs.StringVar(&o.inferenceAddr, "inference-addr", "", "private inference listener (empty = disabled; never route through public ingress)")
	fs.StringVar(&o.inferenceURL, "inference-url", "", "inference origin reachable from worker containers")
	fs.StringVar(&o.inferenceNetworks, "inference-networks", "", "explicit comma-separated worker CIDRs allowed to use inference; direct connection peers only")
	fs.StringVar(&o.restoreGateImage, "restore-gate-image", "", "the forge access gate's image, pinned by digest (<ref>@sha256:...), built with `make gate-image` and pushed where the deployment targets can read it: restored copies of production run behind it; empty = this forge makes no restored copy (it also needs a -public-host origin for the gate to redeem tickets at)")
	fs.StringVar(&o.publicHost, "public-host", "", "the forge's public hostname, or its whole origin (https://forge.example) to state how people reach it: only a stated origin is shown as a link in the guides (empty = localhost only); app ports stay on this host's loopback, so without a -tier-domain preview and deployment addresses answer on this host only, and the UI says so")
	fs.StringVar(&o.mirrorBase, "mirror-base", "", "base URL of the artifact mirror the registry presets point at, e.g. https://nexus.example.com (env FORGE_MIRROR_BASE; empty = no suggestion)")
	fs.StringVar(&o.features, "features", "", "comma-separated feature flags to turn on for this forge (env FORGE_FEATURES), e.g. dev-tools")
	fs.StringVar(&o.otlpEndpoint, "otlp-endpoint", "", "OTLP receiver for forged's own traces, metrics and logs, host:port or URL (env FORGE_OTLP_ENDPOINT); empty sends nothing")
	fs.StringVar(&o.otlpProtocol, "otlp-protocol", "", "OTLP transport: http (default) or grpc (env FORGE_OTLP_PROTOCOL)")
	fs.StringVar(&o.workerURL, "worker-url", "", "a forge-worker to run script tasks on, https://host:port; the forge then runs no script on this host (design/operations/workers.md)")
	fs.StringVar(&o.workerCert, "worker-cert", "", "this installation's client certificate for the worker (PEM)")
	fs.StringVar(&o.workerKey, "worker-key", "", "the key for -worker-cert (PEM)")
	fs.StringVar(&o.workerCA, "worker-ca", "", "the CA that signed the worker's certificate (PEM)")
	fs.StringVar(&o.checkWorker, "check-worker", "", "a forge-worker started with -role check, https://host:port, reached with -worker-cert, -worker-key and -worker-ca: checks run there and nowhere else, and gates accept only its results")
	fs.StringVar(&o.errorsURL, "errors-url", "", "the org's error tracker the error-reporting preset points at (env FORGE_ERRORS_URL)")
	fs.Func("tier-domain", "hostname routing for a tier: preview=<domain>, main=<domain> or eval=<domain> (repeatable, or comma-separated; e.g. preview=preview.example.com — change-97.preview.example.com then routes to that change's preview; eval copies are served on the eval domain and nowhere else)", func(v string) error {
		for _, d := range strings.Split(v, ",") {
			if d = strings.TrimSpace(d); d != "" {
				o.tierDomains = append(o.tierDomains, d)
			}
		}
		return nil
	})
	return o
}

func main() {
	flag.Usage = daemonUsage
	o, showVersion := parseDaemonFlags(flag.CommandLine, os.Args[1:])
	if showVersion {
		fmt.Println("forged", version.String())
		return
	}
	if err := bindEnv(flag.CommandLine, os.Getenv); err != nil {
		log.Fatal(err)
	}

	dataDir, err := filepath.Abs(o.data)
	if err != nil {
		log.Fatal(err)
	}
	if o.taskCPUs <= 0 || o.taskMemoryMiB <= 0 || o.taskPIDs <= 0 || o.chatCPUs <= 0 || o.chatMemoryMiB <= 0 || o.chatPIDs <= 0 {
		log.Fatal("-task-* and -chat-* limits must be above zero: no container runs unbounded")
	}
	cfg := o.forgeConfig(dataDir)
	workerAt := func(url string) *forge.WorkerConfig {
		return &forge.WorkerConfig{URL: url, Cert: o.workerCert, Key: o.workerKey, CA: o.workerCA}
	}
	if o.workerURL != "" || o.checkWorker == "" && (o.workerCert != "" || o.workerKey != "" || o.workerCA != "") {
		cfg.Worker = workerAt(o.workerURL)
	}
	if o.checkWorker != "" {
		cfg.CheckWorker = workerAt(o.checkWorker)
	}
	if o.consoleAddr != "" {
		_, port, err := net.SplitHostPort(o.consoleAddr)
		if err != nil {
			log.Fatalf("console listener: %v", err)
		}
		cfg.ConsoleAPIURL = "http://host.docker.internal:" + port
		if o.consoleURL != "" {
			cfg.ConsoleAPIURL = strings.TrimRight(o.consoleURL, "/")
		}
	} else if o.consoleURL != "" {
		log.Fatal("-console-url needs a console listener (-console-addr)")
	}
	if o.inferenceAddr != "" {
		cfg.InferenceURL = o.inferenceURL
		cfg.InferenceNetworks = strings.Split(o.inferenceNetworks, ",")
	} else if o.inferenceURL != "" || o.inferenceNetworks != "" {
		log.Fatal("inference URL/networks require an inference listener")
	}
	if err := cfg.Validate(); err != nil {
		log.Fatal(err)
	}
	if err := forge.OpenExposure(o.authMode, o.addr, o.consoleAddr, o.openOnNetwork); err != nil {
		log.Fatal(err)
	}
	if flag.NArg() != 0 {
		log.Fatalf("unexpected argument %q", flag.Arg(0))
	}
	if o.backfillMain && (o.verifyOnly || o.drainLocalDocker || o.resetPassword != "") {
		log.Fatal("-backfill-recorded-main writes the record of each project's main; run it on its own, not with -verify-only, -drain-local-docker or -reset-password")
	}
	if o.resetPassword != "" && (o.verifyOnly || o.drainLocalDocker) {
		log.Fatal("-reset-password changes an account; it cannot run with -verify-only or -drain-local-docker")
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		log.Fatal(err)
	}
	// The data directory's layout before anything reads or writes it: a
	// newer build's directory is refused, not misread.
	if err := forge.PrepareDataLayout(dataDir); err != nil {
		log.Fatalf("data directory: %v", err)
	}
	// The process's context before the daemon serves (and for the
	// one-shots): no signal is handled yet, so Ctrl-C ends the process.
	ctx := context.Background()
	// The built-in database comes up BEFORE the unmigrated check, so the
	// refusal below can name a database that is already there to move into.
	if o.dbURL == "" {
		u, err := forge.EnsureBuiltinDB(ctx, dataDir)
		if err != nil {
			log.Fatalf("built-in database: %v", err)
		}
		o.dbURL = u
	}
	// One forged per database (design/subsystems/jobs.md): startup repair
	// treats running work as interrupted, so a second daemon would end the
	// first one's live work. The lock is taken before migrating and held for
	// the process's life. -reset-password runs beside a serving forge
	// (design/operations/README.md) and does not take it.
	if o.holdsDatabase() {
		holdDatabase(ctx, o.dbURL)
	}
	db, err := forge.OpenURL(ctx, o.dbURL)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	app, err := forge.New(ctx, db, cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer forge.BridgeLogs(app)()
	if o.backfillMain {
		// The one-shot runs with no forge serving: its clock is the real one.
		report, err := projectlifecycle.BackfillRecordedMain(ctx, db, dataDir, clock.Or(nil))
		if err != nil {
			log.Fatalf("backfill recorded main: %v", err)
		}
		fmt.Printf("recorded %d, already recorded %d, unknown %d\n", report.Recorded, report.Kept, report.Unknown)
		for _, slug := range report.Unreadable {
			fmt.Printf("unknown: %s (its main could not be read)\n", slug)
		}
		return
	}
	if o.resetPassword != "" {
		// Stdout carries only the password, so it can go straight to a file.
		password, err := app.Access.ResetPassword(ctx, o.resetPassword)
		if err != nil {
			log.Fatalf("reset password for %s: %v", o.resetPassword, err)
		}
		fmt.Println(password)
		log.Printf("reset the password for %s; its sessions have ended", o.resetPassword)
		return
	}
	app.LogStartupConfiguration()
	if o.noLocalDocker {
		log.Printf("no local backend: this forge has no Docker of its own; agents, checks and previews run on the worker, releases on attached Docker hosts")
	} else if o.agentExecutor == forge.ExecutorDocker && !app.DockerAvailable(ctx) {
		log.Printf("WARNING: docker unavailable; model agents cannot run until Docker is available")
	}

	if o.drainLocalDocker {
		if err := app.RecoverInterruptedWork(ctx); err != nil {
			log.Fatal(err)
		}
		log.Printf("drained: no local work or execution container is left on this forge's Docker; it may now start with -no-local-docker")
		return
	}
	if o.verifyOnly {
		// The verification startup (design/subsystems/postgres-migration.md §3.7):
		// reads only, nothing recovered, resumed or scheduled, so the old
		// database stays a lossless rollback until a person accepts.
		log.Printf("forged listening on %s in VERIFY-ONLY mode (data: %s) — reads only; restart without -verify-only to accept", o.addr, dataDir)
		if err := (daemonRuntime{servers: []*http.Server{servedAt(o.addr, forge.ReadOnly(app.Handler()))}}).serveWithSignals(); err != nil {
			log.Fatal(err)
		}
		return
	}
	if o.inferenceAddr == "" {
		log.Printf("inference disabled: model agents require -inference-addr, -inference-url and -inference-networks")
	}
	// Stock data is an explicit normal-startup effect, never constructor work.
	if err := app.Models.SeedFallbacks(ctx); err != nil {
		log.Printf("seeding the slot fallbacks: %v", err)
	}

	if err := recoverThenStart(func() error { return app.RecoverInterruptedWork(ctx) }, func() error {
		app.InstallPanicReporter()
		app.ReapPreviewOrphans(ctx)
		// Publishing stock does not change a project's recorded adoption,
		// except the one-time private-Docker migration, recorded as a revision.
		app.SyncStock(ctx)
		app.RecoverAdoptions() // settle outstanding intents before verifying adoptions
		app.VerifyAdoptions(ctx)
		// A hub left without its hook accepts every push: each is named, so
		// the operator can repair it; the rest of the forge still boots.
		if err := app.InstallNoPushHooks(); err != nil {
			log.Printf("no-push hooks: some project hubs accept pushes:\n%v", err)
		}
		app.SyncSeedIntegrations(ctx)
		app.SeedModelSlots(ctx)
		app.SeedAdministrators(ctx, strings.Split(o.admins, ","))
		if err := app.EnsureAuthorization(ctx); err != nil {
			return fmt.Errorf("authorization setup: %w", err)
		}
		// The forge's UUID exists from its first start: exports, backups and
		// the former ids a cutover keeps are named by it.
		forgeID, err := app.Access.ForgeID(ctx)
		if err != nil {
			return fmt.Errorf("the forge's id: %w", err)
		}
		log.Printf("forge %s", forgeID)
		// After seeding: -admins completes setup, and then no token is due.
		// The log says where the setup token is, never what it is.
		if token, err := app.Access.EnsureSetupToken(ctx); err != nil {
			log.Printf("setup token: %v; the first account cannot be made until it can be written to %s", err, app.Access.SetupTokenPath())
		} else if token != "" {
			log.Printf("no account exists yet: the first one, the administrator, needs the setup token in %s (readable on this host only)", app.Access.SetupTokenPath())
		}
		app.EnsureAgentImage()
		app.SeedTargets(ctx)
		if err := app.RecoverDeployments(ctx); err != nil {
			return err
		}
		if err := app.RecoverLandings(ctx); err != nil {
			return fmt.Errorf("merge inspection: %w", err)
		}
		// Chats before resumed work: a chat that may still write a change's
		// folder must hold that change before anything else can take it.
		if err := app.RecoverConsoles(ctx); err != nil {
			return fmt.Errorf("console recovery: %w", err)
		}
		app.ResumePendingAutoSteps(ctx)
		app.EnsureDeployments(ctx)
		verification.SyncStockScanners(ctx, app.Integrations, stock.FS)
		app.SyncStockDependencies(ctx)
		runtime := daemonRuntime{
			servers: []*http.Server{servedAt(o.addr, app.Handler())},
			// The first remediation pass after a start reconciles what the
			// last process left, without waiting for the schedule.
			start: func() { app.Fixes.Wake(ctx) },
			stop:  app.ShutdownBackground,
			flush: func(ctx context.Context) error { return forge.CloseTelemetry(app, ctx) },
			tasks: []daemonTask{
				{"jobs", app.Jobs().Run},
				{"health controller", daemonLoop(app.RunHealthController)},
				{"deployment observations", daemonLoop(app.Deployment.RunObservations)},
				{"worker previews", daemonLoop(app.Preview.RunWorkerPreviewReconciliation)},
				{"ended previews' keys", daemonLoop(app.Preview.RunPreviewKeyRelease)},
				{"scheduler", daemonLoop(app.RunScheduler)},
			},
		}
		if o.inferenceAddr != "" {
			runtime.servers = append(runtime.servers, &http.Server{Addr: o.inferenceAddr, Handler: app.InferenceHandler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 2 * time.Minute, IdleTimeout: time.Minute, MaxHeaderBytes: 32 << 10})
			log.Printf("private inference API on %s; worker CIDRs %s; direct peers, lifecycle credentials", o.inferenceAddr, o.inferenceNetworks)
		}
		if o.consoleAddr != "" {
			// Console sandboxes use this GET-only listener; both listeners share
			// the same shutdown deadline and failures stop the whole daemon.
			runtime.servers = append(runtime.servers, servedAt(o.consoleAddr, forge.GETOnly(app.Handler())))
			log.Printf("console read-only API on %s", o.consoleAddr)
		}
		log.Printf("forged listening on %s (data: %s)", o.addr, dataDir)
		return runtime.serveWithSignals()
	}); err != nil {
		log.Fatal(err)
	}

	// Process exit is the final boundary, including hijacked connections and
	// older detached work. Do not add an unbounded DB.Close/Wait here after
	// the shared deadline or close resources still used by timed-out work.
}

// forgeConfig is the forge's configuration as the flags state it, before
// the worker, console and inference settings main adds.
func (o *daemonFlags) forgeConfig(dataDir string) forge.Config {
	return forge.Config{
		HostScripts: o.hostScripts,
		TaskLimits:  execution.Limits{CPUs: o.taskCPUs, MemoryBytes: o.taskMemoryMiB << 20, PIDs: o.taskPIDs},
		ChatLimits:  execution.Limits{CPUs: o.chatCPUs, MemoryBytes: o.chatMemoryMiB << 20, PIDs: o.chatPIDs}, Seeding: projectlifecycle.Seeding{AllowPrivate: o.gitImportPrivate, BundleMaxBytes: o.bundleMaxBytes}, VerifyOnly: o.verifyOnly, ConsoleCLI: o.consoleCLI, NoPrivateDocker: o.noPrivateDocker, PrivateDocker: o.privateDocker, NoLocalDocker: o.noLocalDocker, DrainLocalDocker: o.drainLocalDocker, UsingBuiltinDB: o.dbURL == "", DataDir: dataDir, PublicHost: o.publicHost, RestoreGateImage: o.restoreGateImage, ListenAddr: o.addr,
		AgentConcurrency: o.agentConc, AgentExecutor: o.agentExecutor, AgentImage: o.agentImage, AgentModel: o.agentModel, PortainerEnvironment: o.portainerEnv,
		Features: o.features, AuthMode: o.authMode, AuthHeader: o.authHeader, AuthProxySecret: os.Getenv(o.authSecretEnv),
		TrustedProxies: strings.Split(o.trustedProxies, ","), TierDomains: o.tierDomains,
		Defaults:     forge.InstanceDefaults{MirrorBase: o.mirrorBase, ErrorsURL: o.errorsURL},
		OTLPEndpoint: o.otlpEndpoint, OTLPProtocol: o.otlpProtocol}
}

// holdsDatabase says whether this start takes the one-daemon lock: every
// mode that may repair or settle work (serving, -verify-only,
// -drain-local-docker, -backfill-recorded-main) does; only -reset-password,
// documented to run beside a serving forge, does not.
func (o *daemonFlags) holdsDatabase() bool { return o.resetPassword == "" }

// daemonLockKey is the advisory lock a serving forged holds on its database.
const daemonLockKey int64 = 0x666f72676564 // "forged"

// holdDatabase takes the one-daemon lock on url's database or exits naming
// why. The pool it opens keeps the holding connection until the process ends.
func holdDatabase(ctx context.Context, url string) {
	db, err := database.Open(url)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	if _, err := db.HoldExclusive(ctx, daemonLockKey); errors.Is(err, database.ErrHeldElsewhere) {
		log.Fatal("another forged is using this database; stop it first: a second one would treat the first one's running work as interrupted")
	} else if err != nil {
		log.Fatalf("one forged per database: %v", err)
	}
}

// servedAt is the main or console API server. A client that opens a
// connection and sends its headers slowly, or keeps an idle one, is cut
// off; there is no read or write timeout, because a bundle upload, a chat's
// terminal and a long read legitimately take longer.
func servedAt(addr string, h http.Handler) *http.Server {
	return &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
}
