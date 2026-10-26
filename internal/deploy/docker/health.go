package docker

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/dockerlabel"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/strs"
)

// AppTown-owned health controller (design/subsystems/healthchecks.md §6). For the
// deployments the apptown runs itself — standing envs on this docker host — it
// owns the two failures Docker's restart policy gets wrong:
//
//   - Running-but-hung. `--restart unless-stopped` fires on the process
//     EXITING, never on a container that is up and not answering. The
//     controller probes each standing env's public part and, when liveness
//     fails past the app's startup grace, restarts it with capped exponential
//     backoff.
//   - Crash-looping. A process that exits on boot is restarted by Docker
//     forever, with no cap, and is never up long enough for a probe to see
//     it (thousands of restarts can run a host out of memory and take the
//     AppTown down with it). The controller reads Docker's own restart
//     counter and, when it climbs faster than any healthy app restarts,
//     HOLDS the container by stopping it — the one thing unless-stopped
//     honours permanently — and records why. The next deploy replaces it.
//
// Operational contract (§6):
//   - Ownership & persistence: state is DERIVED. On an AppTown restart the loop
//     re-reads the actual containers (their docker StartedAt, their running
//     state) and starts its counters from zero — no persisted counters, so a
//     AppTown bounce triggers no phantom restart and masks no real failure. Only
//     the intent (app.yaml) and the last recorded transition (the ledger)
//     persist.
//   - Backoff: failureThreshold consecutive misses (past startupGrace) →
//     restart, with capped exponential backoff keyed on consecutive restarts;
//     a part that keeps failing is HELD by the cap and surfaced, not tight-looped.
//     successThreshold consecutive passes clears the counters.
//   - Exactly one controller: one goroutine owned by the daemon,
//     acting only on parts THIS apptown runs (the deployments table). A target
//     with its own controller (k8s, a cloud LB) never reaches this loop — the
//     AppTown renders the probe there and steps back.
//   - Drain / readiness: draining is a routing concern and, with a single
//     instance per env today, a no-op (the vhost already surfaces a dead port as
//     502 and re-resolves). It is deferred to replicas; liveness restart is what
//     has teeth now.

const (
	HealthTick             = 15 * time.Second // how often the loop sweeps
	HealthGraceFloor       = 30 * time.Second // startupGrace is never shorter than this on a live env
	HealthFailThreshold    = 3                // consecutive misses before a restart
	HealthSuccessThreshold = 2                // consecutive passes to clear
	HealthBackoffBase      = 15 * time.Second // first backoff after a restart
	HealthBackoffCap       = 5 * time.Minute  // a crash-loop is held here, not hammered
	CrashLoopRestarts      = 5                // Docker restarts within the window that make a crash loop
	CrashLoopWindow        = 10 * time.Minute // Docker restarts an instant exit 7× in 12s; a healthy app never 5× in 10m
)

// partHealth is the controller's derived, in-memory view of one env's
// public part. Nothing here persists; it is rebuilt by observation.
type partHealth struct {
	fails, passes int
	restarts      int       // consecutive restarts, for backoff
	lastRestart   time.Time // when we last restarted it
	// Docker's restart-policy counter as last sampled, and when the sampling
	// window opened, so a crash loop shows as a rate rather than a history.
	restartsSeen int
	windowOpened time.Time
	runtimeToken string
	policy       appcontract.HealthPolicy
	lastProbe    time.Time
}

// Watched is one standing deployment the controller probes.
type Watched struct {
	DeploymentID, ProjectID ids.ID                // the selected records, never resolved again by name
	Identity                deploy.DockerIdentity // the settled deployment and generation on this host
	Slug                    string                // the application
	Name                    string                // the deployment
	HostPort                int
	Container               string
	Docker                  Scope // the daemon it stands on
}

// HealthDependencies is what the controller asks of the world.
type HealthDependencies struct {
	Watch      func(context.Context) []Watched                                           // the standing deployments the apptown runs on hosts it talks to
	PublicPart func(context.Context, Watched) *appcontract.Part                          // the running release's public part, nil when unreadable
	FrontDoor  func() map[string]string                                                  // the front-door signal a probe sends
	Record     func(ctx context.Context, w Watched, kind string, payload map[string]any) // the receipt of a restart or a hold
	Now        func() time.Time                                                          // the clock; nil means time.Now
	// AfterSweep runs once at the end of each sweep: the owner's reading of
	// what the daemons reported since the last (limit events). nil: none.
	AfterSweep func(context.Context)
}

// HealthController owns liveness for the deployments the apptown runs on
// Docker hosts: Docker restarts an exited process but never a
// running-but-hung one, and restarts a crashing one without limit.
type HealthController struct {
	deps  HealthDependencies
	state map[string]*partHealth
}

func NewHealthController(deps HealthDependencies) *HealthController {
	return &HealthController{deps: deps, state: map[string]*partHealth{}}
}

// Seed records observations for a container, for tests of the sweep.
func (c *HealthController) Seed(container string, fails int) {
	c.state[container] = &partHealth{fails: fails}
}

// Watching says whether the controller holds observations for a container.
func (c *HealthController) Watching(container string) bool { _, ok := c.state[container]; return ok }

// Clock replaces the controller's clock, for tests of the crash-loop window.
func (c *HealthController) Clock(now func() time.Time) { c.deps.Now = now }

func (c *HealthController) now() time.Time { return clock.Or(c.deps.Now)() }

// Run sweeps until cancellation.
func (c *HealthController) Run(ctx context.Context) {
	t := time.NewTimer(HealthTick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.Sweep(ctx)
			next := HealthTick
			for _, state := range c.state {
				if state.policy.Explicit {
					next = min(next, max(time.Second, state.lastProbe.Add(state.policy.Period).Sub(c.now())))
				}
			}
			t.Reset(next)
		}
	}
}

// healthSweep probes every standing env's public part once and acts on the
// clear-cut liveness failures. Errors are logged, never fatal — a controller
// that dies is worse than one that misses a tick.
func (c *HealthController) Sweep(ctx context.Context) {
	state := c.state
	if ctx.Err() != nil {
		return
	}
	if c.deps.AfterSweep != nil {
		defer func() {
			if ctx.Err() == nil {
				c.deps.AfterSweep(ctx)
			}
		}()
	}
	envs := c.deps.Watch(ctx)
	live := map[string]bool{}
	for _, e := range envs {
		if ctx.Err() != nil {
			return
		}
		if e.Container == "" {
			continue // targets that manage their own workloads
		}
		live[e.Container] = true
		dk := e.Docker
		ph := state[e.Container]
		if ph == nil {
			ph = &partHealth{}
			state[e.Container] = ph
		}
		// A crash-looping container is mostly between restarts or inside its
		// startup grace, so the probe below never sees it. Docker's counter does.
		if seen, looping := c.crashLooping(ctx, e, dk, ph); looping {
			c.hold(ctx, e, dk, ph, seen)
			continue
		}
		// ensureDeployments owns the missing case, and a stopped container was
		// stopped on purpose — by this controller or by a person — and stays
		// so. Liveness is a question about a running container only.
		if !dk.Running(ctx, e.Container) {
			ph.fails, ph.passes = 0, 0
			continue
		}
		// Within the app's startup grace since the container last started? Don't
		// count failures yet — a first probe before gunicorn/DB are ready is a
		// timing bug the render prevents by construction (§5.3). Re-read from
		// docker so an AppTown restart re-derives this, never a stale memory.
		pub := c.deps.PublicPart(ctx, e)
		if c.explicitLiveness(ctx, e, dk, ph, pub) {
			continue
		}
		if !ph.lastProbe.IsZero() && c.now().Sub(ph.lastProbe) < HealthTick {
			continue
		}
		ph.lastProbe = c.now()
		probe := RenderLivenessProbe(pub)
		grace := probe.Grace
		if grace < HealthGraceFloor {
			grace = HealthGraceFloor
		}
		if within := withinStartupGrace(ctx, dk, e.Container, grace); within {
			continue
		}
		ok := ProbeLiveness(ctx, dk, e.Container, e.HostPort, probe, c.deps.FrontDoor())
		if !ok && ctx.Err() != nil {
			return // a probe the controller's stop cut short is not a failure
		}
		if ok {
			ph.fails = 0
			if ph.passes++; ph.passes >= HealthSuccessThreshold {
				ph.restarts, ph.passes = 0, 0 // recovered — clear the backoff
			}
			continue
		}
		ph.passes = 0
		ph.fails++
		if ph.fails < HealthFailThreshold {
			continue
		}
		// Unhealthy. Respect capped exponential backoff so a crash-loop is held
		// and surfaced, not restarted in a tight loop.
		backoff := HealthBackoffBase << ph.restarts
		if backoff > HealthBackoffCap || backoff <= 0 {
			backoff = HealthBackoffCap
		}
		if !ph.lastRestart.IsZero() && c.now().Sub(ph.lastRestart) < backoff {
			continue // still holding it down
		}
		if ctx.Err() != nil {
			return
		}
		c.restart(ctx, e, dk, ph)
		ph.fails = 0
		ph.restarts++
		ph.lastRestart = c.now()
	}
	// Forget containers that are gone so state can't grow without bound and a
	// re-created env starts fresh.
	for name := range state {
		if !live[name] {
			delete(state, name)
		}
	}
}

// crashLooping samples Docker's restart-policy counter for the container and
// reports whether it rose by CrashLoopRestarts within CrashLoopWindow, with the
// rise seen. The counter is Docker's, not ours: it survives an AppTown restart, is
// kept by docker stop and reset by a manual docker start (measured), so a window that reopens on any drop never mistakes a fresh
// start for a loop and a hold never masks a later one.
func (c *HealthController) crashLooping(ctx context.Context, e Watched, dk Scope, ph *partHealth) (int, bool) {
	count, err := strconv.Atoi(strings.TrimSpace(dk.Text(ctx, "inspect", "-f", "{{.RestartCount}}", e.Container)))
	if err != nil {
		return 0, false // absent or unreadable: nothing to hold
	}
	now := c.now()
	if ph.windowOpened.IsZero() || count < ph.restartsSeen || now.Sub(ph.windowOpened) > CrashLoopWindow {
		ph.restartsSeen, ph.windowOpened = count, now
		return 0, false
	}
	seen := count - ph.restartsSeen
	return seen, seen >= CrashLoopRestarts
}

// hold stops a crash-looping container. docker stop is the one thing
// unless-stopped honours permanently: Docker restarts the process no more, a
// host reboot leaves it stopped, boot reconcile leaves it as found, and the
// next deploy replaces it. The deployment shows as stopped; the ledger says why.
func (c *HealthController) hold(ctx context.Context, e Watched, dk Scope, ph *partHealth, seen int) {
	rt, err := Inspect(ctx, dk, e.Container)
	if err != nil || rt.ID == "" || rt.Labels[dockerlabel.Deployment] != string(e.Identity.Deployment) || rt.Labels[dockerlabel.Generation] != strconv.FormatInt(e.Identity.Generation, 10) {
		log.Printf("health controller: %s/%s crash-loop hold skipped: its recorded container identity could not be witnessed: %v", e.Slug, e.Name, err)
		return
	}
	exit := strings.TrimSpace(dk.Text(ctx, "inspect", "-f", "{{.State.ExitCode}}", rt.ID))
	total := ph.restartsSeen + seen
	log.Printf("health controller: %s/%s is crash-looping — Docker restarted it %d× in %s, %d in all, last exit code %s — stopping it",
		e.Slug, e.Name, seen, CrashLoopWindow, total, exit)
	out, err := dk.Run(ctx, "stop", rt.ID)
	detail := strings.TrimSpace(string(out))
	if err != nil {
		detail = err.Error() + ": " + detail
	}
	// The hold was attempted: its receipt is written though ctx has ended.
	c.deps.Record(context.WithoutCancel(ctx), e, "deployment.crash-loop-held", map[string]any{
		"container":    e.Container,
		"containerId":  rt.ID,
		"restarts":     seen,
		"restartCount": total,
		"window":       CrashLoopWindow.String(),
		"exitCode":     exit,
		"ok":           err == nil,
		"detail":       strs.Truncate(detail, 200),
	})
	if err != nil {
		log.Printf("health controller: stop of %s failed: %v: %s", e.Container, err, strs.Truncate(string(out), 200))
	}
	// The hold is one act: the counter is re-sampled from here, and liveness
	// starts clean if the container is ever started again.
	ph.restartsSeen, ph.windowOpened = total, c.now()
	ph.fails, ph.passes = 0, 0
}

// restart restarts a running-but-unhealthy env container in place: it keeps
// its data volume and network, the least-invasive "restart the part" for a
// single instance (drain-then-replace waits on replicas, §6). It keeps its
// host port only because every standing deployment is published at a fixed
// one (deployment.Deployments.StablePort): Docker picks a port it chose
// itself again at every start, and the probe asks the recorded port (soak
// finding 58). A container from before rc.6 whose only release was its
// first still has a port Docker chose, until it is released again.
func (c *HealthController) restart(ctx context.Context, e Watched, dk Scope, ph *partHealth) {
	log.Printf("health controller: %s/%s failed liveness %d× past grace — restarting (attempt %d)",
		e.Slug, e.Name, ph.fails, ph.restarts+1)
	out, err := dk.Run(ctx, "restart", e.Container)
	// The restart is made: its receipt is written though ctx has ended.
	c.deps.Record(context.WithoutCancel(ctx), e, "deployment.health-restart", map[string]any{
		"container": e.Container,
		"attempt":   ph.restarts + 1,
		"ok":        err == nil,
		"detail":    strs.Truncate(string(out), 200),
	})
	if err != nil {
		log.Printf("health controller: restart of %s failed: %v: %s", e.Container, err, strs.Truncate(string(out), 200))
	}
}

// RenderLivenessProbe derives the LIVENESS probe for the public part — the one
// the health controller (§6) acts on. Liveness drives RESTART, so its default is
// deliberately dependency-free (§8): a TCP connect to the mapped port, which on
// a real host means "the process is up and listening." That default almost never
// false-restarts a healthy app; a part that wants hang-detection declares an
// http/exec liveness explicitly. Never carries checksDeps (guardrail §5.1,
// enforced at parse).
func RenderLivenessProbe(pub *appcontract.Part) RenderedProbe {
	rp := RenderedProbe{Path: "/"}
	policy, err := livenessPolicy(pub)
	rp.PolicyError = err
	if policy.Explicit {
		rp.Policy = &policy
	}
	var l *appcontract.Probe
	if pub != nil && pub.Intent != nil {
		l = pub.Intent.Liveness
	}
	if l == nil {
		rp.TCP = true // §8 default
		return rp
	}
	if l.StartupGrace != "" {
		if d, err := appcontract.ParseHealthDuration(l.StartupGrace); err == nil {
			rp.Grace = d
		}
	}
	switch {
	case l.HTTP != "":
		rp.Path = l.HTTP
		rp.Expect = l.Expect
	case len(l.Exec) > 0:
		rp.Exec = make([]string, len(l.Exec))
		for i, e := range l.Exec {
			rp.Exec[i] = fmt.Sprint(e)
		}
	default: // tcp (or nothing usable) → the safe default
		rp.TCP = true
	}
	return rp
}

// withinStartupGrace reports whether the container last started less than grace
// ago, read from docker (State.StartedAt) so it survives an AppTown restart.
// The start is the Docker host's operating system's stamp, so it is
// compared on the system clock.
func withinStartupGrace(ctx context.Context, dk Scope, container string, grace time.Duration) bool {
	started := dk.Text(ctx, "inspect", "-f", "{{.State.StartedAt}}", container)
	ts, err := time.Parse(time.RFC3339Nano, started)
	if err != nil {
		return false // can't tell → don't suppress a real failure
	}
	return clock.System().Sub(ts) < grace
}

// ProbeLiveness executes one rendered liveness probe against a running env
// container: an in-container exec, a TCP connect to the mapped port, or a
// platform HTTP GET (shaped by AppTown front door so a hardened app doesn't
// 301 the check — the same signal the cold-boot render sends). Any transport
// error, or a declared-status mismatch, is a miss.
func ProbeLiveness(ctx context.Context, dk Scope, container string, hostPort int, rp RenderedProbe, headers map[string]string) bool {
	if rp.PolicyError != nil {
		return false
	}
	timeout := 2 * time.Second
	if rp.Policy != nil {
		timeout = rp.Policy.Timeout
	}
	if rp.Policy != nil || len(rp.Exec) == 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	switch {
	case len(rp.Exec) > 0:
		_, err := dk.Run(ctx, append([]string{"exec", container}, rp.Exec...)...)
		return err == nil
	case rp.TCP:
		conn, err := (&net.Dialer{Timeout: timeout}).DialContext(ctx, "tcp", dk.DialAddress(hostPort))
		if err != nil {
			return false
		}
		conn.Close()
		return true
	default:
		client := &http.Client{
			Transport:     check.ProbeTransport,
			Timeout:       timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
		url := dk.ProbeURL(hostPort) + "/" + trimLeadingSlash(rp.Path)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return false
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := client.Do(req)
		if err != nil {
			return false
		}
		drainAndClose(resp.Body)
		if rp.Expect != 0 {
			return resp.StatusCode == rp.Expect
		}
		return true
	}
}

func trimLeadingSlash(s string) string {
	for len(s) > 0 && s[0] == '/' {
		s = s[1:]
	}
	return s
}
