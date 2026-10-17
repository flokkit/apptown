package forge

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/integration"
	"flokkit.io/forge/internal/forge/wow"
	"flokkit.io/forge/internal/ids"
)

// First-run setup: a fresh forge must not be
// silently inert. The one unavoidable requirement is a model key; the agent
// image can be built from thin air; open auth on a non-loopback listener
// deserves a loud flag. GET /setup reports the state, POST /setup/model
// performs the one-field onboarding.

type setupState struct {
	mu         sync.Mutex
	agentImage string // "present" | "building" | "missing" | "failed" | "skipped" | "no local backend"
	buildErr   string
}

// LogStartupConfiguration reports the final listener's open-auth exposure.
func (a *App) LogStartupConfiguration() {
	addr := a.cfg.listenAddr
	if a.cfg.authMode == "open" && !loopbackAddr(addr) {
		fmt.Fprintf(os.Stderr, "WARNING: forged is listening on %q with open auth — anyone who can reach this port has full control.\n"+
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

// EnsureAgentImage checks for the forge-agent image and builds it from
// deploy/forge-agent in the background when missing — a fresh machine gets
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
	if a.docker.ImageExists(a.life, a.cfg.agentImage) {
		a.setup.mu.Lock()
		a.setup.agentImage = "present"
		a.setup.mu.Unlock()
		return
	}
	ctx := a.cfg.agentContext
	if _, err := os.Stat(filepath.Join(ctx, "Dockerfile")); err != nil {
		a.setup.mu.Lock()
		a.setup.agentImage = "missing"
		a.setup.buildErr = "image " + a.cfg.agentImage + " not found and no " + ctx + " build context beside the binary"
		a.setup.mu.Unlock()
		return
	}
	a.setup.mu.Lock()
	a.setup.agentImage = "building"
	a.setup.mu.Unlock()
	go func() {
		logPath := filepath.Join(a.cfg.dataDir, "agent-image-build.log")
		f, _ := os.Create(logPath)
		req := docker.Command{Args: []string{"build", "-t", a.cfg.agentImage, ctx}}
		if f != nil {
			req.Stdout, req.Stderr = f, f
			defer f.Close()
		}
		_, err := a.docker.Command(a.life, req) // a background build: it stops with the App
		a.setup.mu.Lock()
		if err != nil {
			a.setup.agentImage = "failed"
			a.setup.buildErr = "build failed — see " + logPath
		} else {
			a.setup.agentImage = "present"
		}
		status := a.setup.agentImage
		a.setup.mu.Unlock()
		a.audit(context.WithoutCancel(a.life), "forge-system", ids.Nil, "setup.agent-image", status, nil)
	}()
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
	cfg := integration.SeedConfig{AgentImage: a.cfg.agentImage, AgentModel: a.cfg.agentModel, AgentConcurrency: a.cfg.agentConcurrency}
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
		audit.At(a.cfg.clock.Now()).BestEffort(ctx, db, "forge-system", ids.Nil, k, name, payload)
	})
	log.Print(c)
	a.Integrations.SeedModelBinding(ctx, cfg)
	return c
}
