// apptown-worker runs an AppTown's tasks on a machine that shares no files with
// it (design/operations/workers.md).
// It serves one registered installation over mutual TLS, runs each task on
// its own Docker daemon, and exchanges source and results only as Git
// bundles. No database and no AppTown package: a state directory, a Docker
// daemon and certificates.
//
//	apptown-worker -installation forge-prod -state /var/lib/apptown-worker \
//	  -cert worker.pem -key worker-key.pem -client-ca forge-ca.pem
//
// The Docker daemon is the one DOCKER_HOST names, or the local default.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"flokkit.io/forge/internal/version"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/console/pi"
	dockercli "flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/worker"
)

func main() {
	var (
		listen        = flag.String("listen", ":8443", "address to serve on")
		installation  = flag.String("installation", "", "the one installation this worker serves: its client certificate's Common Name")
		state         = flag.String("state", "", "directory for the worker's records and workspaces")
		certFile      = flag.String("cert", "", "the worker's TLS certificate (PEM)")
		keyFile       = flag.String("key", "", "the worker's TLS key (PEM)")
		clientCA      = flag.String("client-ca", "", "the CA that signs the installation's client certificate (PEM)")
		docker        = flag.String("docker", "docker", "the docker binary")
		dindImage     = flag.String("dind-image", "docker:27-dind", "the image of a chat's or a task's private Docker daemon")
		agentContext  = flag.String("agent-context", "deploy/apptown-agent", "the agent image's build context; the worker builds apptown-agent:<hash of it> when it starts (empty: build none)")
		chatCPUs      = flag.Float64("chat-cpus", 2, "CPUs for each of a chat's container and its private Docker daemon")
		chatMemory    = flag.Int64("chat-memory-mib", 4096, "memory, swap included, in MiB, for each of a chat's container and its private Docker daemon")
		chatPIDs      = flag.Int64("chat-pids", 4096, "processes for each of a chat's container and its private Docker daemon")
		serviceCPUs   = flag.Float64("service-cpus", execution.DefaultServiceLimits.CPUs, "CPUs for each service (a test database and the like) run beside a check or a task")
		serviceMemory = flag.Int64("service-memory-mib", execution.DefaultServiceLimits.MemoryBytes>>20, "memory, swap included, in MiB, for each service run beside a check or a task")
		servicePIDs   = flag.Int64("service-pids", execution.DefaultServiceLimits.PIDs, "processes for each service run beside a check or a task")
		exclusive     = flag.Bool("exclusive", false, "declare that this machine is the installation's alone: nothing else runs on it. Recorded in the worker's profile with every private-Docker attempt; it is not measured")
		checkMetadata = flag.Bool("check-metadata", false, "say whether a container on this host reaches the cloud metadata service ("+metadataAddress+"), then exit; the install blocks it")
		showVersion   = flag.Bool("version", false, "print this build's version and AppTown protocols it serves, and exit")
		roleFlag      = flag.String("role", worker.RoleAgent, "agent: chats, tasks and previews; check: checks and nothing else. Fixed on the first start with an empty -state, never changed")
	)
	flag.Parse()
	// The worker process's context: it runs until the process is killed.
	ctx := context.Background()
	if *showVersion {
		fmt.Println(versionLine())
		return
	}
	if *checkMetadata {
		fmt.Println("the cloud metadata service from a container: " + metadataReach(ctx, dockercli.CLI{Binary: *docker}, *dindImage))
		return
	}
	if *installation == "" || *state == "" || *certFile == "" || *keyFile == "" || *clientCA == "" {
		fmt.Fprintln(os.Stderr, "apptown-worker needs -installation, -state, -cert, -key and -client-ca")
		os.Exit(2)
	}
	// A chat's container, its daemon and a save's container each get these:
	// twice them ordinarily, three times while a save runs. Zero is never
	// "unlimited".
	if *chatCPUs <= 0 || *chatMemory <= 0 || *chatPIDs <= 0 {
		fmt.Fprintln(os.Stderr, "apptown-worker: -chat-cpus, -chat-memory-mib and -chat-pids must be above zero")
		os.Exit(2)
	}
	// Each service a check or a task runs beside it gets these, so one
	// project's test database cannot starve the worker. Zero is never
	// "unlimited".
	if *serviceCPUs <= 0 || *serviceMemory <= 0 || *servicePIDs <= 0 {
		fmt.Fprintln(os.Stderr, "apptown-worker: -service-cpus, -service-memory-mib and -service-pids must be above zero")
		os.Exit(2)
	}
	serviceLimits := execution.Limits{CPUs: *serviceCPUs, MemoryBytes: *serviceMemory << 20, PIDs: *servicePIDs}
	// The worker's one clock: what it stamps and judges times by.
	now := clock.Or(nil)
	// The role is fixed before anything else touches the state directory.
	role, err := worker.FixRole(*state, *roleFlag, now)
	if err != nil {
		log.Fatalf("apptown-worker: %v", err)
	}
	cert, err := tls.LoadX509KeyPair(*certFile, *keyFile)
	if err != nil {
		log.Fatalf("loading the worker's certificate: %v", err)
	}
	caPEM, err := os.ReadFile(*clientCA)
	if err != nil {
		log.Fatalf("reading the client CA: %v", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		log.Fatalf("%s holds no certificate", *clientCA)
	}
	if err := os.MkdirAll(*state, 0o700); err != nil {
		log.Fatal(err)
	}
	// One download cache for the toolchains projects declare, shared by
	// every container here (worker/tool_cache.go). Removing what is in it
	// is safe while nothing runs; mise fetches again.
	toolCache := filepath.Join(*state, "tool-cache", "mise")
	if err := os.MkdirAll(toolCache, 0o755); err != nil {
		log.Fatal(err)
	}
	log.Printf("apptown-worker: tool cache %s holds %d MiB", toolCache, worker.ToolCacheSize(toolCache)>>20)
	// A chat's image and mirror come with each session from AppTown; its
	// resolvers are this machine's. Its caches are its own, removed with it,
	// but for the worker's tool cache.
	sessions := pi.New(pi.Config{
		Run:             pi.CLI(*docker),
		Binary:          *docker, // saves and terminals run it directly
		Image:           func() string { return "" },
		Dind:            pi.DindConfig{Image: *dindImage, Mark: worker.PrivilegedMark(*state, role, now)},
		CachePerSession: true,
		Mounts:          []string{worker.ToolCacheVolume(toolCache)},
		Limits:          &pi.Limits{CPUs: *chatCPUs, MemoryBytes: *chatMemory << 20, PIDs: *chatPIDs},
	})
	// The agent image is built before the worker serves: a chat asks for
	// the image its AppTown's own build context names, and one this worker
	// cannot provide is refused with the reason.
	agentImage := ""
	if *agentContext != "" {
		if image, err := worker.BuildAgentImage(ctx, *docker, *agentContext); err != nil {
			log.Printf("apptown-worker: no agent image; chats will be refused: %v", err)
		} else {
			log.Printf("apptown-worker: agent image %s", image)
			agentImage = image
		}
	}
	// Previews are reached at the ports they publish on this machine's
	// loopback: a Docker on another machine runs none.
	var previews dockercli.Client = dockercli.CLI{Binary: *docker}
	if host := os.Getenv("DOCKER_HOST"); host != "" && !strings.HasPrefix(host, "unix://") {
		log.Printf("apptown-worker: Docker is at %s, not on this machine; this worker runs no previews", host)
		previews = nil
	}
	w := &worker.Server{Installation: *installation, Role: role, State: *state, Tasks: execution.DockerTasks{Binary: *docker, CallerAdmitsDisk: true}, Sessions: sessions,
		Images: worker.DockerImages{Binary: *docker}, AgentImage: agentImage, ImageLabels: worker.DockerImages{Binary: *docker}, Checks: execution.Runner{DockerBinary: *docker, ServiceLimits: serviceLimits}, Previews: previews,
		DindImage: *dindImage, Docker: dockercli.CLI{Binary: *docker}, ServiceLimits: serviceLimits, ToolCache: toolCache, Now: now}
	// What private Docker rests on is measured now, and reported with what
	// the operator declares; AppTown records it with every attempt
	// admitted with private Docker (worker/profile.go). A check worker runs
	// none.
	if role.Role == worker.RoleAgent {
		w.Profile = w.MeasureProfile(ctx, *exclusive)
		log.Printf("apptown-worker: profile %+v", w.Profile)
	}
	// A check the worker was running when it stopped is discarded, and each
	// minute anything past its deadline goes (worker/checks.go); a preview
	// whose boot the stop interrupted goes, and a failed removal is tried
	// again each minute (worker/previews.go); a task's private Docker goes
	// once its admission has expired (worker/private.go); the build cache is
	// kept within its budget.
	w.SweepChecks(ctx, dockercli.CLI{Binary: *docker}, true)
	w.SweepPreviews(ctx, true)
	go func() {
		for range time.Tick(time.Minute) {
			w.SweepChecks(ctx, dockercli.CLI{Binary: *docker}, false)
			w.SweepPreviews(ctx, false)
			w.SweepPrivate(ctx)
			w.TrimBuildCache(ctx)
		}
	}()
	if err := w.ReapSessions(ctx); err != nil {
		log.Printf("apptown-worker: cleaning up after sessions: %v", err)
	}
	srv := &http.Server{
		Addr:              *listen,
		Handler:           w.Handler(),
		TLSConfig:         worker.TLSConfig(cert, pool),
		ReadHeaderTimeout: 30 * time.Second,
	}
	log.Printf("apptown-worker (%s, since %s) serving installation %s on %s", role.Role, role.Since.Format(time.RFC3339), *installation, *listen)
	if err := srv.ListenAndServeTLS("", ""); !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

// versionLine is what -version prints: the build and AppTown protocols
// this worker serves.
func versionLine() string {
	return fmt.Sprintf("apptown-worker %s (serves AppTown protocols %d to %d)", version.String(), worker.ProtocolMin, worker.ProtocolMax)
}
