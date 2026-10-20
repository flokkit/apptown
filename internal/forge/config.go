package forge

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/forge/projectlifecycle"

	"flokkit.io/forge/internal/console"
	"flokkit.io/forge/internal/deploy/digitalocean"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/forge/integration"
	"flokkit.io/forge/internal/forge/secret"
	"flokkit.io/forge/internal/worker"

	"flokkit.io/forge/internal/forge/inference"

	"flokkit.io/forge/internal/forge/deployment"
)

// Config is construction input, not live application state. New validates and
// copies it; changing this value afterwards cannot reconfigure an instance.
type Config struct {
	// DockerClient supplies the command transport before owners are wired.
	// Nil selects the production CLI. Changing this input later does not
	// replace the client held by an already constructed App.
	DockerClient docker.Client
	// ConsoleAgent supplies the external session runtime before owners are wired.
	// Nil builds Pi; the caller owns an injected agent's lifetime. Forge still
	// prepares every launch environment. Later changes do not reconfigure App.
	ConsoleAgent console.Agent
	// ConsoleWorker supplies the runtime for chats on a worker before owners
	// are wired, for tests; nil builds it from Worker when one is set.
	ConsoleWorker *worker.RemoteAgent
	// DigitalOcean supplies external driver options at construction. The zero
	// value keeps driver defaults; later changes do not reconfigure App.
	DigitalOcean digitalocean.Options
	// DockerProbe supplies readiness timing before deployment owners are wired.
	// Nil uses production timing; replacing this input later has no effect.
	DockerProbe *docker.Probe
	// SSHConfigPath is the ssh client configuration AppTown writes each
	// attached Docker host's stanza into. Empty is ~/.ssh/config, the file
	// ssh itself reads; tests give each AppTown its own.
	SSHConfigPath string
	// ProcessRunner selects the process executable before consumers are wired.
	// Its zero value uses production defaults; later changes do not reach App.
	ProcessRunner execution.Runner
	// SecretsHTTPClient supplies the secret provider transport at construction.
	// Nil lets the secret owner create its bounded default client. Replacing
	// this input later does not replace a constructed App's client.
	SecretsHTTPClient secret.HTTPClient
	// Seeding is what a project that starts from existing code may fetch
	// and upload (-git-import-allow-private, -import-bundle-max-bytes).
	Seeding projectlifecycle.Seeding
	// VerifyOnly refuses startup repair; the daemon also serves reads only.
	VerifyOnly bool
	// UsingBuiltinDB assigns instance database inventory and backups to Forge.
	UsingBuiltinDB bool
	// HostAddresses snapshots trusted local proxy addresses at construction.
	// Nil discovers local interfaces; an empty slice disables host discovery.
	// Loopback and TrustedProxies remain trusted. Address bytes are copied.
	HostAddresses   []net.IP
	NoPrivateDocker bool
	// PrivateDocker admits private Docker (docker: true) for agent steps on
	// a worker: the operator's opt-in. Each attempt records whether the
	// worker's measured profile is a qualified one (design/operations/workers.md,
	// "Private Docker on the worker").
	PrivateDocker bool
	// NoLocalDocker runs AppTown with no local Docker daemon and no
	// project process on its own host (-no-local-docker): its own Docker
	// and its project-process runner refuse, attached targets keep their
	// transport, and a worker and an external database are required
	// (design/operations/workers.md, "Running AppTown without a local Docker
	// daemon").
	NoLocalDocker bool
	// DrainLocalDocker makes startup repair the witnessed switch into the
	// no-local-docker mode (-drain-local-docker): see drainLocalDocker.
	DrainLocalDocker bool
	// LocalTLS is this AppTown serving itself over HTTPS from its own local
	// certificate authority (-local-tls, the desktop trial); nil otherwise.
	LocalTLS   *console.LocalTLS
	DataDir    string
	PublicHost string
	// RestoreGateImage is the access gate's image by digest
	// (-restore-gate-image): without it, and a -public-host origin for the
	// gate to redeem tickets at, AppTown makes no restored copy.
	RestoreGateImage string
	ListenAddr       string
	ConsoleAPIURL    string
	ConsoleCLI       string
	AuthMode         string
	AuthHeader       string
	AuthProxySecret  string
	AgentExecutor    string
	AgentImage       string
	// AgentContext is the agent image's build context, relative to the
	// forge's working directory: AppTown builds it, and a worker's image
	// is named by it (worker.AgentImage). "" is deploy/apptown-agent.
	AgentContext     string
	AgentModel       string
	AgentConcurrency int
	// PortainerEnvironment is the Portainer environment the seeded intranet
	// target releases to (the portainer-deploy binding's environment). ""
	// is integration.DefaultPortainerEnvironment.
	PortainerEnvironment string
	Features             string
	TierDomains          []string
	TrustedProxies       []string
	Defaults             InstanceDefaults
	InferenceURL         string
	InferenceNetworks    []string
	// OTLPEndpoint turns apptownd's own telemetry on: host:port, or a URL whose
	// scheme says whether the receiver speaks TLS. Empty means nothing is sent.
	OTLPEndpoint string
	OTLPProtocol string // http (default) or grpc
	// TaskLimits bound every step, check and task container, here or on a
	// worker (-task-cpus, -task-memory-mib, -task-pids); ChatLimits bound a
	// chat's container on this host (-chat-*). Zero fields take the
	// execution defaults: no container runs unbounded.
	TaskLimits execution.Limits
	ChatLimits execution.Limits
	// HostScripts lets a script step with `executor: local` run as a process
	// on this host (-host-scripts). Off, a project's script runs only in a
	// container.
	HostScripts bool
	// Worker, when set, is the apptown-worker that runs this installation's
	// script tasks (design/operations/workers.md). The installation is then
	// isolated: script steps run only on the worker, never on AppTown's own
	// host.
	Worker *WorkerConfig
	// CheckWorker, when set, is a worker provisioned with the check role:
	// every check runs there, and gates accept only its results
	// (design/operations/workers.md, "A check worker").
	CheckWorker *WorkerConfig
}

// WorkerConfig reaches a apptown-worker: its address, the installation's
// client certificate and key, and the CA that signed the worker's
// certificate. All four are required.
type WorkerConfig struct{ URL, Cert, Key, CA string }

// client builds the worker's task driver, loading the certificates now so a
// missing or unreadable file stops startup.
func (w WorkerConfig) client() (*execution.WorkerTasks, error) {
	if w.URL == "" || w.Cert == "" || w.Key == "" || w.CA == "" {
		return nil, fmt.Errorf("a worker needs its address, -worker-cert, -worker-key and -worker-ca")
	}
	if !strings.HasPrefix(w.URL, "https://") {
		return nil, fmt.Errorf("the worker address %q must be https", w.URL)
	}
	cert, err := tls.LoadX509KeyPair(w.Cert, w.Key)
	if err != nil {
		return nil, fmt.Errorf("loading the worker client certificate: %w", err)
	}
	caPEM, err := os.ReadFile(w.CA)
	if err != nil {
		return nil, fmt.Errorf("reading the worker CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("%s holds no certificate", w.CA)
	}
	// No overall timeout: waiting for a task holds the request open for the
	// task's whole run, and the caller's context bounds it.
	transport := &http.Transport{TLSClientConfig: &tls.Config{Certificates: []tls.Certificate{cert}, RootCAs: pool, MinVersion: tls.VersionTLS13},
		IdleConnTimeout: 90 * time.Second}
	return &execution.WorkerTasks{URL: w.URL, Client: &http.Client{Transport: worker.SpeakProtocol(transport)}}, nil
}

type configuration struct {
	verifyOnly, usingBuiltinDB                                      bool
	seeding                                                         projectlifecycle.Seeding
	noPrivateDocker, privateDocker, noLocalDocker, drainLocalDocker bool
	dataDir, publicHost, listenAddr, consoleAPIURL                  string
	publicHostScheme                                                string // as -public-host stated it; empty for a bare name
	restoreGateImage                                                string
	sshConfigPath                                                   string
	consoleCLI                                                      string
	authMode, authHeader, authProxySecret                           string
	agentExecutor, agentImage, agentModel                           string
	agentContext                                                    string
	agentConcurrency                                                int
	portainerEnvironment                                            string
	taskLimits, chatLimits                                          execution.Limits
	hostScripts                                                     bool
	localTLS                                                        *console.LocalTLS
	features                                                        map[string]bool
	tierDomains                                                     map[string]tierDomain
	proxies                                                         proxyPolicy
	defaults                                                        InstanceDefaults
	inferenceURL                                                    string
	otlpEndpoint, otlpProtocol                                      string
	inferenceNetworks                                               []netip.Prefix
	worker                                                          *execution.WorkerTasks // nil: tasks run on this host
	checkWorker                                                     *execution.WorkerTasks // nil: checks run where tasks do
	clock                                                           *forgeClock
}

// forgeClock is AppTown's clock, which the owners whose rules store a
// time and judge it later (a project's deletion grace and kept period, a
// deployment's copies, drills and restored copies) are given: the wall
// clock, which tests move forward (ahead) rather than wait days.
type forgeClock struct {
	mu    sync.Mutex
	ahead time.Duration
}

func (c *forgeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return clock.Or(nil)().Add(c.ahead)
}

// Validate checks configuration before database setup or any startup effects.
func (c Config) Validate() error { _, err := c.prepare(); return err }

func (c Config) prepare() (configuration, error) {
	switch c.AuthMode {
	case "", "open":
		c.AuthMode = "open"
	case "internal", "trusted-header":
	default:
		return configuration{}, fmt.Errorf("unknown -auth mode %q (open | internal | trusted-header)", c.AuthMode)
	}
	switch c.AgentExecutor {
	case "":
		c.AgentExecutor = ExecutorDocker
	case ExecutorDocker, ExecutorLocal:
	default:
		return configuration{}, fmt.Errorf("unknown -agent-executor %q (%s | %s)", c.AgentExecutor, ExecutorDocker, ExecutorLocal)
	}
	// Admitting and refusing private Docker at once has no meaning; neither
	// flag is allowed to win silently.
	if c.PrivateDocker && c.NoPrivateDocker {
		return configuration{}, fmt.Errorf("-private-docker and -no-private-docker contradict each other; give at most one")
	}
	if c.AgentContext == "" {
		c.AgentContext = "deploy/apptown-agent"
	}
	if c.AgentImage == "" {
		c.AgentImage = "apptown-agent"
	}
	if c.PortainerEnvironment == "" {
		c.PortainerEnvironment = integration.DefaultPortainerEnvironment
	}
	if c.AgentConcurrency == 0 {
		c.AgentConcurrency = 2
	}
	if c.AgentConcurrency < 1 {
		c.AgentConcurrency = 1
	}
	if c.ConsoleAPIURL == "" {
		c.ConsoleAPIURL = "http://host.docker.internal:7778"
	}
	features, err := parseFeatures(c.Features)
	if err != nil {
		return configuration{}, err
	}
	publicHostScheme, publicHost, err := parsePublicHost(c.PublicHost)
	if err != nil {
		return configuration{}, err
	}
	tiers, err := parseTierDomains(c.TierDomains)
	if err != nil {
		return configuration{}, err
	}
	var networks []netip.Prefix
	if proto := strings.ToLower(strings.TrimSpace(c.OTLPProtocol)); proto != "" && proto != "http" && proto != "grpc" {
		return configuration{}, fmt.Errorf("unknown -otlp-protocol %q (http | grpc)", c.OTLPProtocol)
	}
	if c.InferenceURL != "" || len(c.InferenceNetworks) != 0 {
		networks, err = inference.ParseConfig(c.InferenceURL, c.InferenceNetworks)
		if err != nil {
			return configuration{}, err
		}
	}
	if c.DrainLocalDocker && (c.NoLocalDocker || c.VerifyOnly) {
		return configuration{}, fmt.Errorf("-drain-local-docker observes AppTown's own Docker: run it without -no-local-docker or -verify-only")
	}
	if c.NoLocalDocker && (c.Worker == nil || c.UsingBuiltinDB) {
		return configuration{}, fmt.Errorf("-no-local-docker needs a worker (-worker-url) and an external database (-db-url): this AppTown runs nothing on its own Docker, and the built-in database is a container there")
	}
	// The worker drivers judge a task's expiry on the clock that wrote it.
	clock := &forgeClock{}
	var worker, checkWorker *execution.WorkerTasks
	if c.Worker != nil {
		if worker, err = c.Worker.client(); err != nil {
			return configuration{}, err
		}
		worker.Now = clock.Now
	}
	if c.CheckWorker != nil {
		if checkWorker, err = c.CheckWorker.client(); err != nil {
			return configuration{}, err
		}
		checkWorker.Now = clock.Now
	}
	return configuration{clock: clock, worker: worker, checkWorker: checkWorker, seeding: c.Seeding, verifyOnly: c.VerifyOnly, usingBuiltinDB: c.UsingBuiltinDB, noPrivateDocker: c.NoPrivateDocker, privateDocker: c.PrivateDocker, noLocalDocker: c.NoLocalDocker, drainLocalDocker: c.DrainLocalDocker, localTLS: c.LocalTLS, dataDir: c.DataDir, sshConfigPath: c.SSHConfigPath, publicHost: publicHost, publicHostScheme: publicHostScheme, listenAddr: c.ListenAddr, restoreGateImage: strings.TrimSpace(c.RestoreGateImage),
		consoleAPIURL: c.ConsoleAPIURL, consoleCLI: c.ConsoleCLI, authMode: c.AuthMode, authHeader: c.AuthHeader, authProxySecret: c.AuthProxySecret,
		agentExecutor: c.AgentExecutor, agentImage: c.AgentImage, agentContext: c.AgentContext, agentModel: c.AgentModel, agentConcurrency: c.AgentConcurrency, portainerEnvironment: c.PortainerEnvironment,
		taskLimits: withDefaults(c.TaskLimits, execution.DefaultTaskLimits), chatLimits: withDefaults(c.ChatLimits, execution.DefaultChatLimits), hostScripts: c.HostScripts,
		features: features, tierDomains: tiers, proxies: proxyPolicy{networks: parseCIDRs(c.TrustedProxies)}, defaults: c.Defaults,
		inferenceURL: c.InferenceURL, inferenceNetworks: networks, otlpEndpoint: strings.TrimSpace(c.OTLPEndpoint), otlpProtocol: strings.ToLower(strings.TrimSpace(c.OTLPProtocol))}, nil
}

// parsePublicHost reads -public-host: a bare name ("forge.example", with
// an optional port), or a whole origin ("https://forge.example") that also
// states how people reach it. It returns the stated scheme (empty for a bare
// name) and the host, which is what every other consumer of the setting uses.
func parsePublicHost(raw string) (scheme, host string, err error) {
	if !strings.Contains(raw, "://") {
		return "", raw, nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return "", "", fmt.Errorf("-public-host wants a name (forge.example) or an origin (https://forge.example), got %q", raw)
	}
	return u.Scheme, u.Host, nil
}

// publicOrigin is AppTown's own web origin when -public-host stated it
// whole ("https://forge.example"), or empty. The scheme is never inferred:
// the tier domains say how deployed apps are reached, not AppTown, and an
// origin guessed as http would send people's tokens in the clear.
func (c configuration) publicOrigin() string {
	if c.publicHost == "" || c.publicHostScheme == "" {
		return ""
	}
	return c.publicHostScheme + "://" + c.publicHost
}

// routing passes only immutable ingress facts to deployment consumers. Prefer
// preview then main, making the proxy and boot probe agree even with mixed tiers.
func (c configuration) routing() deployment.Routing {
	preview, main := c.tierDomains["preview"], c.tierDomains["main"]
	scheme := preview.Scheme
	if scheme == "" {
		scheme = main.Scheme
	}
	eval := c.tierDomains["eval"]
	if scheme == "" {
		scheme = eval.Scheme
	}
	if scheme == "" {
		scheme = "http"
	}
	return deployment.Routing{PublicHost: c.publicHost, Scheme: scheme, PreviewScheme: preview.Scheme, PreviewDomain: preview.Domain, MainScheme: main.Scheme, MainDomain: main.Domain,
		EvalScheme: eval.Scheme, EvalDomain: eval.Domain}
}

// InstanceDefaults: the facts that vary per installation and used to be
// compiled into the catalog — one installation's mirror and error-tracker
// hostnames showed on the first screen of every other instance. Set from
// apptownd's flags; empty means "no suggestion".
type InstanceDefaults struct {
	MirrorBase string // e.g. https://nexus.example.com — presets carry only the repository path
	ErrorsURL  string // the org's error tracker, e.g. https://errors.example.com
}

// withDefaults fills each zero field of l from d.
func withDefaults(l, d execution.Limits) execution.Limits {
	if l.CPUs <= 0 {
		l.CPUs = d.CPUs
	}
	if l.MemoryBytes <= 0 {
		l.MemoryBytes = d.MemoryBytes
	}
	if l.PIDs <= 0 {
		l.PIDs = d.PIDs
	}
	return l
}
