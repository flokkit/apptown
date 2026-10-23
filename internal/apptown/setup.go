package apptown

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/trust"
)

// First-run setup: a fresh AppTown must not be
// silently inert. The one unavoidable requirement is a model key; the agent
// image can be built from thin air; open auth on a non-loopback listener
// deserves a loud flag. GET /setup reports the state, POST /setup/model
// performs the one-field onboarding.

type setupState struct {
	mu         sync.Mutex
	agentImage string // "present" | "building" | "pulling" | "missing" | "failed" | "skipped" | "no local backend"
	buildErr   string
}

// LogStartupConfiguration reports the final listener's open-auth exposure.
func (a *App) LogStartupConfiguration() {
	addr := a.cfg.listenAddr
	if a.cfg.authMode == "open" && !loopbackAddr(addr) {
		fmt.Fprintf(os.Stderr, "WARNING: apptownd is listening on %q with open auth — anyone who can reach this port has full control.\n"+
			"         Bind to 127.0.0.1, or configure -auth trusted-header behind an authenticating proxy.\n", addr)
	}
}

func loopbackAddr(addr string) bool {
	host := addr
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		host = addr[:i]
	}
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// OpenExposure refuses open authentication on a listener another machine
// can reach unless the operator said so (-open-on-network): in open mode a
// caller names itself, so reaching the port is full control. An empty auth
// mode is open mode, as Config reads it; an empty main address is every
// interface, as net.Listen binds it; an empty console address is no console
// listener.
func OpenExposure(authMode, addr, consoleAddr string, allowed bool) error {
	if (authMode != "" && authMode != "open") || allowed {
		return nil
	}
	var exposed []string
	if addr == "" || !loopbackAddr(addr) {
		exposed = append(exposed, fmt.Sprintf("-addr %q", addr))
	}
	if consoleAddr != "" && !loopbackAddr(consoleAddr) {
		exposed = append(exposed, fmt.Sprintf("-console-addr %q", consoleAddr))
	}
	if len(exposed) == 0 {
		return nil
	}
	sort.Strings(exposed)
	return fmt.Errorf("open authentication lets any caller act as anyone, and %s can be reached from other machines: bind it to 127.0.0.1, use -auth internal, or pass -open-on-network to accept that anyone who reaches the port has full control", strings.Join(exposed, " and "))
}

// EnsureAgentImage checks for AppTown-agent image and builds it from
// deploy/apptown-agent in the background when missing, or when it lacks
// AppTown's version label — a fresh machine gets
// a working executor without reading a single doc. Skipped when docker is
// unavailable or the build context isn't shipped alongside the binary.
func (a *App) EnsureAgentImage() {
	if a.docker.Absent() { // the worker builds and reports its own agent image
		a.setup.mu.Lock()
		a.setup.agentImage = "no local backend"
		a.setup.mu.Unlock()
		return
	}
	if !a.DockerAvailable(a.life) {
		a.setup.mu.Lock()
		a.setup.agentImage = "skipped"
		a.setup.mu.Unlock()
		return
	}
	// An image without AppTown's version label was not built from this
	// release's recipe (one built before 1.0 carries forge.pi.version): it
	// is fetched again, so every step records which tools it ran.
	if v := a.docker.AgentRuntimeVersion(a.life, a.cfg.agentImage); a.docker.ImageExists(a.life, a.cfg.agentImage) && v != "" && v != "<no value>" {
		a.setup.mu.Lock()
		a.setup.agentImage = "present"
		a.setup.mu.Unlock()
		return
	}
	a.setup.fetch(a.life, a.docker, a.cfg.agentImage, a.cfg.agentContext, filepath.Join(a.cfg.dataDir, "agent-image-build.log"), func(status string) {
		a.audit(context.WithoutCancel(a.life), "apptown-system", ids.Nil, "setup.agent-image", status, nil)
	})
}

// fetch gets a missing agent image in the background, recording its state
// as it goes and reporting where it ended; it stops with ctx. An image
// named in a registry is pulled, never built: a local build would carry a
// name that says it is the published image. So the pull needs no build
// context.
func (s *setupState) fetch(ctx context.Context, dk docker.Scope, image, buildContext, logPath string, settled func(status string)) {
	pull := namesRegistry(image)
	if !pull {
		if _, err := os.Stat(filepath.Join(buildContext, "Dockerfile")); err != nil {
			s.mu.Lock()
			s.agentImage = "missing"
			s.buildErr = "image " + image + " not found and no " + buildContext + " build context beside the binary"
			s.mu.Unlock()
			return
		}
	}
	s.mu.Lock()
	s.agentImage = "building"
	if pull {
		s.agentImage = "pulling"
	}
	s.mu.Unlock()
	go func() {
		f, _ := os.Create(logPath)
		var out io.Writer
		if f != nil {
			out = f
			defer f.Close()
		}
		err := fetchAgentImage(ctx, dk, image, buildContext, pull, out)
		s.mu.Lock()
		if err != nil {
			s.agentImage = "failed"
			s.buildErr = err.Error() + " — see " + logPath
		} else {
			s.agentImage = "present"
		}
		status := s.agentImage
		s.mu.Unlock()
		settled(status)
	}()
}

// fetchAgentImage pulls the agent image (pull) or builds it from the
// build context, writing Docker's output to out when it is given.
func fetchAgentImage(ctx context.Context, dk docker.Scope, image, buildContext string, pull bool, out io.Writer) error {
	// The extra certificates as the build's secret: its RUN lines download
	// (internal/trust, deploy/apptown-agent/Dockerfile).
	req := docker.Command{Args: append(append([]string{"build"}, trust.BuildArgs(!dk.Remote())...), "-t", image, buildContext)}
	what := "build"
	if pull {
		req, what = docker.Command{Args: []string{"pull", image}}, "pull of "+image
	}
	if out != nil {
		req.Stdout, req.Stderr = out, out
	}
	if _, err := dk.Command(ctx, req); err != nil {
		return fmt.Errorf("%s failed", what)
	}
	return nil
}

// namesRegistry says whether an image reference names a registry, by
// Docker's rule: the first path component is a registry host when it has a
// dot or a port, or is localhost ("ghcr.io/flokkit/apptown-agent:1.0.0",
// "localhost:5000/agent"); "apptown-agent" and "library/ubuntu" do not.
func namesRegistry(ref string) bool {
	first, _, ok := strings.Cut(ref, "/")
	return ok && (strings.ContainsAny(first, ".:") || first == "localhost")
}

func (s *setupState) ImageState() (string, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.agentImage, s.buildErr
}

// SeedTargets records the targets that exist at boot (deployment's rule).
func (a *App) SeedTargets(ctx context.Context) { a.Deployment.SeedTargets(ctx, a.tierDomainNames()) }

func (a *App) tierDomainNames() []string {
	var out []string
	for tier, d := range a.cfg.tierDomains {
		out = append(out, tier+"="+d.Domain)
	}
	return out
}

// SyncSeedIntegrations syncs integration's seed bindings as a versioned
// stock kind, under the same contract as the ways of working (wow.Sync),
// then seeds the model binding. Call after agent defaults are set (main).
func (a *App) SyncSeedIntegrations(ctx context.Context) wow.SyncCounts {
	cfg := integration.SeedConfig{AgentImage: a.cfg.agentImage}
	var items []wow.SyncItem
	for _, s := range integration.Seeds(cfg) {
		items = append(items, wow.SyncItem{Name: s.Name, Content: s.Config, Data: s})
	}
	bindings, db := a.Integrations.Bindings, a.DB
	kind := wow.SyncKind{
		Label: "integration",
		Current: func(name string) (string, bool, bool, error) {
			st, err := bindings.StockCurrent(ctx, name)
			if err != nil || st == nil {
				return "", false, false, err
			}
			return st.Content, st.Managed, true, nil
		},
		Insert: func(it wow.SyncItem) error {
			s := it.Data.(integration.Seed)
			return bindings.StockInsert(ctx, it.Name, s.Capability, s.Provider, it.Content)
		},
		Update:         func(it wow.SyncItem) (int, error) { return bindings.StockUpdate(ctx, it.Name, it.Content) },
		ReceiptUpdated: "integration.seed.updated",
		Versioned:      true,
	}
	c := wow.Sync(kind, items, func(k, name string, payload map[string]any) {
		audit.At(a.cfg.clock.Now()).BestEffort(ctx, db, "apptown-system", ids.Nil, k, name, payload)
	})
	log.Print(c)
	a.Integrations.SeedModelBinding(ctx)
	return c
}
