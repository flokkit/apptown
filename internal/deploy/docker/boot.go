package docker

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"slices"
	"strings"
	"time"

	"flokkit.io/forge/internal/check"
	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/deploy/appcontract"
	"flokkit.io/forge/internal/strs"
)

// The one boot sequence (deployment.md). Five paths used to assemble it by
// hand and every delivery incident lived in the assembly. Now:
//
//	resolve   read the needs, fulfil them for the docker provider, merge
//	          their connection env over the contract's
//	needs     start the stack, throwaway for a preview, standing for a
//	          deployment, and wait for it to be healthy
//	init      run the init containers to completion, in order
//	parts     start the non-public app parts on the network
//	app       start the public app container
//	probe     an HTTP answer from the app itself, alive after a settle
//
// BootRequest carries what varies between the paths; nothing else does.

// BootRequest names what a boot is for. Everything the sequence needs beyond
// the contract itself.
type BootRequest struct {
	// Log reports phase entry. It receives no environment values or command output.
	// Nil disables progress reporting without changing boot behavior.
	Log       deploy.Logf
	Slug      string // the application's slug
	DataClass string // what a standing deployment holds; "" or "synthetic" lets a moved service be recreated
	Name      string // the public app container: forge-preview-change-7, forge-env-<slug>-main
	StackID   string // owner of its stack (services, init, network): pvsnap-change-7, pv<step>, env-<slug>-<env>
	Workspace string // the tree to boot: a worktree, a run workspace, the project checkout
	// HostPort pins a standing deployment's stable port; 0 lets docker pick
	// (previews).
	HostPort int
	// Persistent marks a standing deployment: its needs are reused when
	// already running (data survives releases), everything restarts with the
	// host, and the labels say class=env — never reaped. Previews are the
	// opposite on every count.
	Persistent bool
	// Deployment names which deployment's secret bindings apply
	//: "preview" for every preview, the standing
	// deployment's name for a deploy or rebuild. Empty means preview.
	Deployment string
	// Services overrides the contract's sidecars (a workflow's services:
	// declaration); empty means the contract's own.
	Services []deploy.ServiceSpec
	Probe    time.Duration
	// Docker is which daemon the stack boots on: the forge's own by default,
	// an attached host's for a standing deployment bound there.
	Docker Scope
	// PageCheck is the gate's extra question about the page a visitor
	// opens: a client-only site's page answers (below 400), and a
	// server app's root is not refused (check.PageRefusal). Gates ask it;
	// the try-it button and the rebuild do not.
	PageCheck bool
	// VisitorHost is the host name a visitor's request carries through the
	// front door (change-7.preview.example, shop.apps.example.com); the page
	// check sends it, so a host allow-list is judged as a visitor meets it.
	// "" sends the probe address's own.
	VisitorHost  string
	ReleaseImage string // Recorded public artifact for a historical release preview.
	// Incarnation names what the boot serves, for a preview: its source and
	// the digest of its prepared public values. It is labelled on the app
	// container (forge.incarnation), and a reuse matches it. "" is none.
	Incarnation string
	// Scratch is the boot's own folder, where a prepared boot keeps its
	// Docker configuration; its owner sweeps it (SweepBootConfigs). A
	// prepared boot without one is refused.
	Scratch  string
	Identity *deploy.DockerIdentity // Standing execution from a selected source; previews have no deployment identity.
	// Recorded is a release's images: IDs already on this daemon, or images
	// by digest it pulls. The boot runs them and builds nothing
	// (PrepareImages), for a standing deployment or a release's preview.
	// nil prepares each image from the source.
	Recorded *RecordedImages
	// Revision is the committed revision a standing deployment serves. A
	// public static part is told its release identity
	// (appcontract.Contract.PublicRelease), answers it at ReleasePath, and
	// the boot witnesses that answer. "" (a preview of a worktree) tells it
	// nothing and witnesses nothing.
	Revision string
	// Record records what a standing deployment's boot creates (its
	// containers, a network it made, data volumes, the images it pulled or
	// built), as each is created: its take-down deletes exactly that. nil
	// records nothing (previews and runs have their own cleanup). A
	// creation that cannot be recorded fails the boot.
	Record func(deploy.Owned) error
	// Owned is the deployment's list before this boot (Ownership.Recorded).
	Owned []deploy.Owned
	// Sealed boots a restore rehearsal: no address outside its host, its
	// own network, its needs filled before any init step, and only the
	// public part (Sealed). nil boots as before.
	Sealed *Sealed
	// release is PublicRelease(Revision), set by Boot.
	release string
	// own is Record with the needs' data paths, set by Boot.
	own Ownership
	// browser is a static part's browser values, prepared by Boot before any
	// effect; nil makes StartAppContainer read them itself.
	browser *string
}

func (t BootRequest) logf(format string, args ...any) {
	if t.Log != nil {
		t.Log(format, args...)
	}
}

// VisitorHost is the Host a visitor's request carries at an address, for a
// gate's page check (BootRequest.VisitorHost).
func VisitorHost(address string) string {
	u, err := url.Parse(address)
	if err != nil {
		return ""
	}
	if u.Port() == "0" { // an address whose port is not yet known
		return u.Hostname()
	}
	return u.Host
}

// BootResult is what a boot produced. On a failure after the app started
// (probe, static check) HostPort and Network are still filled so the caller
// can name the URL or tear the stack down.
type BootResult struct {
	HostPort int
	Network  string
	Stack    ResolvedStack
	// Page is what the front-page check saw, passed or refused; nil when
	// the boot did not ask (no PageCheck, or it stopped before).
	Page *PageWitness
}

// PageWitness is the front-page check's record: the path that answered,
// the host name the request carried, the status, and the verdict. A pass
// is evidence like a refusal, not the absence of one.
type PageWitness struct {
	Path   string `json:"path"`
	Host   string `json:"host,omitempty"`
	Status int    `json:"status,omitempty"` // 0: no answer
	Passed bool   `json:"passed"`
	// Verdict says what the answer means for a visitor, in a sentence.
	Verdict string `json:"verdict"`
}

// Image is the public part's image id, as booted.
func (r BootResult) Image() string { return r.Stack.Plan.PublicPart().Image }

// Boot phases, on BootError. The callers word their own failures per phase
// — the step evidence, the button's sentence and the rebuild log all differ.
const (
	PhaseResolve = "resolve" // an unknown need: refused at the door
	PhaseSecrets = "secrets" // a declared secret nothing provides, or a broken declaration
	PhaseConfig  = "config"  // the values a browser may be given could not be read
	PhaseNeeds   = "needs"   // the stack did not start
	PhaseFill    = "fill"    // a sealed boot's needs could not be filled from the copy
	PhaseParts   = "parts"   // init containers or non-public parts
	PhaseApp     = "app"     // the public app container did not start
	PhaseProbe   = "probe"   // it started but never answered (LogTail has its last words)
	PhasePage    = "page"    // it answered, but the page a visitor opens is refused or missing
)

type BootError struct {
	Phase   string
	Err     error
	LogTail string
}

func (e *BootError) Error() string { return e.Err.Error() }

// BeforeEffects says the boot failed before touching Docker: the attempt's
// outcome is known, nothing changed, and a new attempt may follow once the
// cause is fixed. A later phase can have left effects nobody saw.
func (e *BootError) BeforeEffects() bool {
	return e.Phase == PhaseResolve || e.Phase == PhaseSecrets || e.Phase == PhaseConfig
}
func (e *BootError) Unwrap() error { return e.Err }

// asBootError: the phase-carrying error behind a boot failure; any other
// error comes back wrapped with no phase.
func AsBootError(err error) *BootError {
	var be *BootError
	if errors.As(err, &be) {
		return be
	}
	return &BootError{Err: err}
}

// ResolvedStack: what a contract runs beside, fulfilled for the docker
// provider — the one answer every path (and the intranet's compose) shares.
type ResolvedStack struct {
	// Plan owns the resolved execution values for each consumer.
	Plan *appcontract.Plan
	// Services are the sidecars (the contract's, or a workflow override)
	// followed by the needs' emulators — the stack, in start order.
	Services []deploy.ServiceSpec
	// Needs are the fulfilled needs (the tail of Services) with where each
	// emulator keeps its data; the intranet gives each a named volume.
	Needs []ResolvedNeed
	// Secrets is the broker's delivery for this boot: per consumer, the
	// values to inject; and the receipts (names, stores, versions — never
	// values) the boot records.
	Secrets Delivery
}

// ResolvedNeed is a fulfilled need plus its emulator's data path.
type ResolvedNeed struct {
	deploy.ServiceSpec
	Data string
}

// resolveStack reads the contract's needs and fulfils them locally: emulator
// containers plus the env the app reads to use them. In cloud a provider
// fulfils the same needs with real resources; the app code is identical.
// An unknown need is refused with the one sentence every surface uses.
func (h *Host) ResolveStack(ctx context.Context, c *appcontract.Contract, override []deploy.ServiceSpec) (ResolvedStack, error) {
	depSvcs, depEnv, err := h.deps.Needs.Fulfil(ctx, c.Needs, c.NeedParams)
	if err != nil {
		return ResolvedStack{}, err
	}
	depSvcs, err = sizedServices(c, depSvcs)
	if err != nil {
		return ResolvedStack{}, err
	}

	rs := ResolvedStack{Plan: appcontract.Prepare(c, depEnv)}
	rs.Services = append(append([]deploy.ServiceSpec{}, override...), depSvcs...)
	for _, s := range depSvcs {
		n := ResolvedNeed{ServiceSpec: s}
		data, err := h.deps.Needs.Data(ctx, s.Name)
		if err != nil {
			return ResolvedStack{}, fmt.Errorf("where %s keeps its data: %w", s.Name, err)
		}
		n.Data = data
		rs.Needs = append(rs.Needs, n)
	}
	return rs, nil
}

// BootLifetime bounds a preview's boot, from its first command to its
// answer: builds, pulls, init containers and the probe all end with it
// (Scope.WithLifetime), and a boot's configuration is never in use longer.
const BootLifetime = 30 * time.Minute

// Boot runs the sequence for one target. The order is the contract:
// needs healthy → init to completion → parts → app → probe. What it says
// of a failure (the error, the app's last words, the front-page verdict)
// never holds a value the forge generated for the boot's needs: those
// become Redacted wherever the boot runs, so no record or answer made from
// it carries one (deploy.ServiceSpec.Credentials).
func (h *Host) Boot(ctx context.Context, t BootRequest, c *appcontract.Contract) (BootResult, error) {
	res, err := h.boot(ctx, t, c)
	redact := credentialRedactor(res.Stack.Services)
	if res.Page != nil {
		res.Page.Verdict = redact(res.Page.Verdict)
	}
	return res, redactBootError(err, redact)
}

// Redacted is what a generated credential reads as in a boot's failure.
const Redacted = "[secret]"

// credentialRedactor replaces each generated credential of services with
// Redacted, longest first, so one inside another is not half-replaced.
func credentialRedactor(services []deploy.ServiceSpec) func(string) string {
	var values []string
	for _, s := range services {
		values = append(values, s.Credentials...)
	}
	if len(values) == 0 {
		return func(text string) string { return text }
	}
	slices.SortFunc(values, func(x, y string) int { return len(y) - len(x) })
	pairs := make([]string, 0, 2*len(values))
	for _, v := range values {
		if v != "" {
			pairs = append(pairs, v, Redacted)
		}
	}
	r := strings.NewReplacer(pairs...)
	return r.Replace
}

// redactBootError is err saying what it said with redact applied: its
// phase and what it wraps are kept, so callers still tell its kind.
func redactBootError(err error, redact func(string) string) error {
	if err == nil {
		return nil
	}
	be := AsBootError(err)
	text := err.Error()
	if redact(text) == text && redact(be.LogTail) == be.LogTail {
		return err
	}
	return &BootError{Phase: be.Phase, LogTail: redact(be.LogTail), Err: redactedError{text: redact(text), err: err}}
}

// redactedError reads as its redacted text and unwraps to what it was.
type redactedError struct {
	text string
	err  error
}

func (e redactedError) Error() string { return e.text }
func (e redactedError) Unwrap() error { return e.err }

// boot is Boot before its failure is redacted.
func (h *Host) boot(ctx context.Context, t BootRequest, c *appcontract.Contract) (BootResult, error) {
	var res BootResult
	t.logf("Resolving runtime configuration and container limits")
	// A prepared boot's Docker configuration is its own: its one registry
	// login, written before any phase builds, pulls or runs, and gone with
	// the boot. It reads nothing of the machine's.
	if login, ok := h.deps.Registry.(preparedLogin); ok {
		dk, done, err := t.Docker.WithBootConfig(ctx, t.Scratch, login.c)
		if err != nil {
			return res, &BootError{Phase: PhaseResolve, Err: fmt.Errorf("a Docker configuration for the boot: %w", err)}
		}
		defer func() {
			// A folder that stays is swept (SweepBootConfigs); say so.
			if err := done(); err != nil {
				log.Printf("removing a boot's Docker configuration: %v", err)
			}
		}()
		t.Docker = dk
	}
	// A part or need larger than this daemon can give any container is
	// refused before anything starts (AdmitResources).
	if err := AdmitResources(ctx, t.Docker, ResourceAdmissionContract(c, t.Services)); err != nil {
		return res, &BootError{Phase: PhaseResolve, Err: err}
	}
	rs, err := h.ResolveStack(ctx, c, t.Services)
	if err != nil {
		return res, &BootError{Phase: PhaseResolve, Err: err}
	}
	// From here a container's output may quote the needs' credentials: what
	// a failure keeps of it is redacted before it is cut short.
	t.Docker.Redact = credentialRedactor(rs.Services)
	// Secrets: the declaration read from the tree being booted, each
	// secret's binding for THIS deployment resolved, and every consumer
	// handed only what its scope names. A required secret nothing provides
	// stops the boot here, before a container exists.
	env := t.Deployment
	if env == "" {
		env = "preview"
	}
	t.logf("Resolving the app's secret bindings for %s", env)
	rs.Secrets, err = h.deps.Secrets.Resolve(ctx, t.Slug, env, t.Workspace, c)
	if err != nil {
		return res, &BootError{Phase: PhaseSecrets, Err: err}
	}
	res.Stack = rs
	t.own = Ownership{Record: t.Record, Needs: map[string]string{}, Recorded: t.Owned}
	for _, n := range rs.Needs {
		t.own.Needs[n.Name] = n.Data
	}
	// A static site's browser values are read before anything in the stack
	// changes: a failed read must not leave migrations run and workers
	// replaced behind a still-serving old app.
	if c.PublicPart().IsStatic() {
		browser, err := h.browserEnv(ctx)
		if err != nil {
			return res, &BootError{Phase: PhaseConfig, Err: fmt.Errorf("reading the values a browser may be given: %w", err)}
		}
		t.browser = &browser
	}
	plan := rs.Plan
	wasStatic := c.PublicPart().IsStatic()
	t.release = c.PublicRelease(t.Revision)
	if t.ReleaseImage != "" {
		plan, err = deploy.PreparedReleaseApp(plan, t.ReleaseImage)
		if err != nil {
			return res, &BootError{Phase: PhaseResolve, Err: err}
		}
	}
	// New work on a daemon short of disk is refused before anything is
	// pulled, built or started (AdmitDisk), after every other refusal that
	// needs no Docker; what runs now is not touched.
	if err := AdmitDisk(ctx, t.Docker); err != nil {
		return res, &BootError{Phase: PhaseResolve, Err: err}
	}
	if t.Identity != nil || t.Recorded != nil {
		plan, err = h.PrepareImages(ctx, t, plan)
		if err != nil && t.Recorded != nil {
			return res, &BootError{Phase: PhaseResolve, Err: err} // only asked the daemon: nothing changed
		}
		if err != nil {
			return res, &BootError{Phase: PhaseApp, Err: err}
		}
	}

	network := ""
	if len(rs.Services) > 0 {
		t.logf("Starting or reusing %d dependency services", len(rs.Services))
		if t.Persistent {
			// The env's standing rail: a running sidecar is reused, so its
			// postgres (and its data) survives releases.
			var serr error
			network, serr = EnsureStandingServices(ctx, t.Docker, t.StackID, rs.Services, t.DataClass, t.own)
			if serr != nil {
				return res, &BootError{Phase: PhaseNeeds, Err: serr}
			}
			t.logf("Waiting for dependency service health checks")
			if _, err := waitStackHealthy(ctx, t.Docker, rs.Services, t.StackID); err != nil {
				return res, &BootError{Phase: PhaseNeeds, Err: err}
			}
		} else {
			// A throwaway stack on a preview-scoped network — --rm and blank
			// each boot; reaped with the preview and swept at restart.
			network, _, err = StartServices(ctx, t.Docker, t.StackID, rs.Services)
			if err != nil {
				return res, &BootError{Phase: PhaseNeeds, Err: err}
			}
		}
	}
	res.Network = network
	if t.Sealed != nil {
		// A sealed stack always has its own network, made here when no
		// need made one, and its needs are filled before anything reads
		// them; only the public part runs (sealed.go).
		if network == "" {
			network = NetworkName(t.StackID)
			if err := ensureNetwork(ctx, t.Docker, network, t.own); err != nil {
				return res, &BootError{Phase: PhaseNeeds, Err: err}
			}
			res.Network = network
		}
		if t.Sealed.Fill != nil {
			if err := t.Sealed.Fill(network); err != nil {
				return res, &BootError{Phase: PhaseFill, Err: err}
			}
		}
		plan = plan.PublicOnly()
	}

	// needs are up → init containers run to completion (migrations) → the
	// non-public parts start; only the execution plan receives built images.
	t.logf("Running initialization steps and starting background app parts")
	plan, network, err = bringUpParts(ctx, t.Docker, t.StackID, t.Name, t.Workspace, network, plan, rs.Secrets, t.own)
	if err != nil {
		return res, &BootError{Phase: PhaseParts, Err: err}
	}
	res.Network, res.Stack.Plan = network, plan

	t.logf("Starting the public app container (%s)", plan.PublicPart().Name)
	hostPort, err := h.StartAppContainer(ctx, t, plan.PublicPart(), network,
		rs.Secrets.EnvFor(plan.PublicPart().Name))
	if err != nil {
		return res, &BootError{Phase: PhaseApp, Err: err}
	}
	res.HostPort = hostPort
	t.logf("Checking application readiness")
	if t.Sealed != nil {
		// Nothing outside the host can reach the app, the forge's probe
		// included: it asks from inside the stack's network.
		rp := RenderColdBootProbe(plan.PublicPart(), h.frontDoor())
		wait := t.Probe
		if rp.Grace > wait {
			wait = rp.Grace
		}
		if !t.Sealed.awaitReady(ctx, t.Docker, network, t.Name, plan.PublicPart().Port, rp, wait) {
			return res, &BootError{Phase: PhaseProbe, Err: fmt.Errorf("the app did not answer %s on its own network", strs.FirstNonBlank(rp.Path, "/"))}
		}
		return res, nil
	}

	probeU := t.Docker.ProbeURL(hostPort)
	// Render the cold-boot readiness probe from the public part's intent, shaped
	// by the forge's front-door signal (design/subsystems/healthchecks.md §4.3). cold-boot,
	// preview and main all reach the app through forge's own reverse proxy, so
	// the ingress env is `forge`.
	probe := RenderColdBootProbe(plan.PublicPart(), h.frontDoor())
	if up, logTail := h.prober().Check(ctx, t.Docker, t.Name, probeU, t.Probe, probe); !up {
		return res, &BootError{Phase: PhaseProbe, Err: errors.New("app did not come up in a cold container"), LogTail: logTail}
	}
	// A static site answers through the forge's file server configuration,
	// which its image carries (StaticSiteDockerfile). An image built before
	// the image carried it has none (the command was added at boot then): run
	// as it is, the file server's stock configuration answers its default
	// page to every probe of "/". A self-serving image told its release is
	// witnessed answering it at ReleasePath, as App Platform witnesses it;
	// otherwise only that configuration answers /_apptown/env.js.
	selfServing := t.Recorded == nil || t.Recorded.SelfServing
	switch {
	case t.release != "" && selfServing:
		if up, logTail := h.prober().Check(ctx, t.Docker, t.Name, probeU, t.Probe, RenderedProbe{Path: appcontract.ReleasePath, Expect: 200, Body: t.release}); !up {
			return res, &BootError{Phase: PhaseProbe, Err: fmt.Errorf("the static site's file server does not answer %s with this release (%s): its image does not carry AppTown's file server configuration, or it serves another release", appcontract.ReleasePath, t.release), LogTail: logTail}
		}
	case wasStatic || plan.PublicPart().ServesStatic:
		if up, logTail := h.prober().Check(ctx, t.Docker, t.Name, probeU, t.Probe, RenderedProbe{Path: "/_apptown/env.js", Expect: 200}); !up {
			return res, &BootError{Phase: PhaseProbe, Err: errors.New("the static site's file server does not answer /_apptown/env.js: its image does not carry AppTown's file server configuration (an image built before 3 October 2026 does not); release it again to rebuild the image"), LogTail: logTail}
		}
	}
	if t.PageCheck {
		t.logf("Checking the page visitors open")
	}
	if t.PageCheck && wasStatic {
		status, ok, why := staticPageServes(probeU)
		res.Page = &PageWitness{Path: "/", Host: hostOf(probeU), Status: status, Passed: ok, Verdict: why}
		if !ok {
			return res, &BootError{Phase: PhasePage, Err: errors.New(why)}
		}
	} else if t.PageCheck {
		// The readiness path is often exempt from what refuses visitors
		// (Rails /up skips host authorization): ask the root as a visitor
		// arrives, through the same front-door shaping.
		host := t.VisitorHost
		if host == "" {
			host = hostOf(probeU)
		}
		status, path, err := check.ProbePage(ctx, check.PageProbe{URL: probeU, Host: t.VisitorHost, Headers: h.frontDoor()})
		if err != nil {
			why := fmt.Sprintf("GET / as a visitor got no answer: %v", err)
			res.Page = &PageWitness{Path: path, Host: host, Verdict: why}
			return res, &BootError{Phase: PhasePage, Err: errors.New(why), LogTail: containerLogTail(ctx, t.Docker, t.Name)}
		}
		why := check.PageRefusal(status, path)
		res.Page = &PageWitness{Path: path, Host: host, Status: status, Passed: why == "", Verdict: why}
		if why != "" {
			return res, &BootError{Phase: PhasePage, Err: errors.New(why), LogTail: containerLogTail(ctx, t.Docker, t.Name)}
		}
		res.Page.Verdict = fmt.Sprintf("GET %s answered %d: a visitor who opens the app is not refused", path, status)
	}
	return res, nil
}
