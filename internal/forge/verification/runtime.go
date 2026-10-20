package verification

// The runtime evaluator (design/subsystems/conformance.md §2 "runtime"; healthchecks.md
// §5.2): a check judged against the app as it actually RUNS — the project's
// standing main deployment — not its tree. A fleet review probes the
// deployment the forge itself serves, through the same two doors production
// traffic and probes use: the FRONT DOOR (the public URL, TLS terminated by the
// forge's proxy, X-Forwarded-* set) and NAKED (loopback to the mapped port, no
// front-door signal at all). A readiness endpoint that answers 200 both ways
// is redirect-safe, which is the portability contract §5.2 states; one that
// 301s the naked probe is the office-poll ten-commit thrash waiting to happen
// on any target whose probe cannot carry headers.
//
// The same target resolution is the hook for runtime scanners (a DAST tool
// pointed at the front door) — standingTarget is the one place that knows
// where a project's running app is. The probe grammar itself, and the
// judgement over the two doors' answers, are the library's (internal/check):
// neither needs a project. What stays here is resolving WHERE the two doors
// are, and what path this app declares as its readiness.

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/forge/preview"

	"flokkit.io/forge/internal/check"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/deploy/appcontract"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/forge/conformance"
	"flokkit.io/forge/internal/forge/deployment"
)

// Runtime reads a standing deployment and probes its declared readiness.
// The daemon scope is shared explicitly so instance-local injection and updates
// have the same visibility as other callers; it cannot reach any App operation.
type Runtime struct {
	deployment    *deployment.Service
	hosts         *deployment.Hosts
	db            *database.DB
	docker        *docker.Scope
	dataDir       string
	browserValues func(ctx context.Context, projectID ids.ID) (map[string]string, error)
}

type RuntimeDependencies struct {
	// Deployment observes the standing deployment on its own target and
	// says where it is reached.
	Deployment *deployment.Service
	// Hosts reaches the daemon a standing deployment runs on.
	Hosts   *deployment.Hosts
	DB      *database.DB
	Docker  *docker.Scope
	DataDir string
	// BrowserValues is what the integration settings give a project's page:
	// the trusted expectation the browser-env check judges a response against.
	BrowserValues func(ctx context.Context, projectID ids.ID) (map[string]string, error)
}

func NewRuntime(d RuntimeDependencies) *Runtime {
	return &Runtime{deployment: d.Deployment, hosts: d.Hosts, db: d.DB, docker: d.Docker, dataDir: d.DataDir, browserValues: d.BrowserValues}
}

// repo is a project's repository in this forge's data directory.
func (o Runtime) repo(slug string) string {
	return filepath.Join(o.dataDir, "workspaces", "project-"+slug)
}

// browserExpectation is what the browser-env check judges against, from
// trusted sources only: the contract the target runs says whether it is a
// static site, and the integration settings say what its page must be given.
// A recorded release whose contract cannot be read is unknown, never the
// working tree's contract instead: a server contract in the workspace would
// pass an unreadable static release.
func (o Runtime) browserExpectation(ctx context.Context, t Target) (check.BrowserExpectation, string) {
	var c *appcontract.Contract
	var err error
	switch {
	case t.Recorded != nil:
		c = t.Recorded.Contract
	case t.ContractDir != "":
		c, err = docker.ReadWorkspaceContract(t.ContractDir)
	case t.Slug != "" && t.ReleaseSHA == "":
		return check.BrowserExpectation{}, "this deployment does not record which revision it runs, so whether it is a static site is not known; deploy it again"
	case t.Slug != "":
		c, err = docker.ReadContractAt(ctx, o.repo(t.Slug), t.ReleaseSHA)
	default:
		return check.BrowserExpectation{}, "the app's contract is not known, so whether it is a static site is not known"
	}
	if err != nil || c == nil || c.PublicPart() == nil {
		return check.BrowserExpectation{}, "the app's contract could not be read, so whether it is a static site is not known"
	}
	want := check.BrowserExpectation{Static: c.PublicPart().IsStatic()}
	if !want.Static {
		return want, ""
	}
	if t.Recorded != nil {
		// The values the preview was booted with, as the forge prepared them.
		values := map[string]string{}
		if err := json.Unmarshal([]byte(t.Recorded.BrowserEnv), &values); err != nil {
			return want, "the values the preview was booted with cannot be read: " + err.Error()
		}
		want.Attached = values
		return want, ""
	}
	if o.browserValues == nil {
		return want, "AppTown cannot read what its integrations give the page"
	}
	values, err := o.browserValues(ctx, t.ProjectID)
	if err != nil {
		return want, "reading what the integrations give the page: " + err.Error()
	}
	want.Attached = values
	return want, ""
}

// judgeBrowser judges a target's page against what the forge gives it. What
// the container was started with is read from that exact container, before
// and after the probe: a container replaced in between makes the answer
// unknown, never a verdict about the wrong one.
func (o Runtime) judgeBrowser(ctx context.Context, t Target) check.ProbeVerdict {
	want, why := o.browserExpectation(ctx, t)
	if why != "" {
		return check.ProbeVerdict{Outcome: check.CannotAssess, Detail: why, Fault: true}
	}
	host := o.docker
	if t.Host != nil {
		host = t.Host
	}
	if !want.Static || len(want.Attached) == 0 || (host == nil && t.Served == nil) || t.Container == "" {
		return check.JudgeBrowserEnv(ctx, check.HTTPFetcher, t.Naked, want)
	}
	installed := func() (docker.InstalledBrowser, error) {
		if t.Served != nil {
			return t.Served.Browser(ctx)
		}
		return docker.InspectBrowserValues(ctx, *host, t.Container)
	}
	before, err := installed()
	if err != nil {
		return check.JudgeBrowserEnv(ctx, check.HTTPFetcher, t.Naked, want) // Installed stays unknown
	}
	want.Installed = before.Values
	verdict := check.JudgeBrowserEnv(ctx, check.HTTPFetcher, t.Naked, want)
	if after, err := installed(); err != nil || after.ID != before.ID {
		return check.ProbeVerdict{Outcome: check.CannotAssess, Detail: "the deployment was replaced while it was being checked; check it again", Fault: true}
	}
	return verdict
}

// Target is where a project's standing main deployment answers.
type Target struct {
	ProjectID ids.ID
	Slug      string
	Container string
	HostPort  int
	FrontDoor string // the public URL: through the forge's proxy, or an attached host's own
	Naked     string // the mapped port directly: loopback here, the attached host by address
	// Host is the daemon a standing deployment's container runs on, its
	// own target's; nil is the forge's own.
	Host        *docker.Scope
	ContractDir string // when set, the tree whose .apptown/app.yaml declares readiness (a change's committed tree at the gate)
	ReleaseSHA  string
	// Served is a preview opened for probing, wherever it runs: what its
	// container was started with is read through it.
	Served *preview.Served
	// Recorded is what the preview was booted with, as this forge recorded
	// it: its contract and public values. When set, readiness and browser
	// expectations come from it, never from today's settings or another tree.
	Recorded *docker.PublicPrepared
	// Deployment is the standing deployment the target is, when it is one:
	// what the forge applied to it and observes of it answer the
	// standing-only probes. Nil for a preview.
	Deployment *deployment.Deployment
	// Revision is the commit, or the default branch, a contract judged as
	// the project stands is read at: a review's recorded source.
	Revision string
}

// standingTarget resolves a project's running main deployment, or says why
// there is none to probe. Whether it runs is the deployment owner's
// observation on the deployment's own target (ObserveContainer): an attached
// host is asked over its own connection, and what cannot be observed is
// unknown or unavailable, never "not running".
func (o Runtime) standingTarget(ctx context.Context, projectID ids.ID) (Target, *deployment.Deployment, string, bool) {
	var t Target
	t.ProjectID = projectID
	e, err := o.deployment.Deployments.ByName(ctx, projectID, "main")
	if err == nil {
		p := projectOrEmpty(ctx, o.db, projectID)
		t.Slug, t.Revision, t.Container, t.HostPort, t.ReleaseSHA = p.Slug, p.DefaultBranch, e.Container, e.HostPort, e.ReleaseSHA
	}
	if err != nil || t.Container == "" {
		return t, nil, "no standing main deployment — deploy the app to be judged at runtime", false
	}
	switch observed := o.deployment.ObserveContainer(ctx, e); observed.State {
	case deployment.ContainerRunning:
	case deployment.ContainerStopped, deployment.ContainerGone:
		return t, e, fmt.Sprintf("the main deployment is not running (%s) — nothing to probe", observed.State), false
	default:
		return t, e, fmt.Sprintf("whether the main deployment is running is %s: %s", observed.State, observed.Reason), false
	}
	host, err := o.hosts.DockerScopeFor(ctx, e.TargetID)
	if err != nil {
		return t, e, "whether the main deployment is running is unknown: " + err.Error(), false
	}
	t.Host = &host
	t.FrontDoor = o.deployment.URL(ctx, t.Slug, e)
	t.Naked = host.ProbeURL(t.HostPort)
	t.Deployment = e
	return t, e, "", true
}

// mainAt resolves main's standing deployment for comparison with a change's
// base: a target, the commit it serves as the deployment owner witnesses it
// (ServedRevision), and a recheck that says the same container still serves
// that commit after a probe. A deployment that cannot be resolved, or whose
// commit cannot be named, is a reason instead.
func (o Runtime) mainAt(ctx context.Context, projectID ids.ID) (t Target, commit string, still func() bool, why string) {
	t, e, why, ok := o.standingTarget(ctx, projectID)
	if !ok {
		return t, "", nil, why
	}
	served := o.deployment.ServedRevision(ctx, e)
	if served.Commit == "" {
		return t, "", nil, "which commit main's deployment runs is not known: " + served.Reason
	}
	still = func() bool {
		again := o.deployment.ServedRevision(ctx, e)
		return again.Commit == served.Commit && again.ContainerID == served.ContainerID
	}
	return t, served.Commit, still, ""
}

// declaredReadinessPath is the public part's declared readiness HTTP path at
// the commit the deployment serves (falling back to the repo head).
func (o Runtime) declaredReadinessPath(ctx context.Context, t Target) (string, bool) {
	repo := o.repo(t.Slug)
	var c *appcontract.Contract
	var err error
	if t.ReleaseSHA != "" {
		c, err = docker.ReadContractAt(ctx, repo, t.ReleaseSHA)
	}
	if c == nil || err != nil {
		c, err = docker.ReadWorkspaceContract(repo)
	}
	if err != nil || c == nil {
		return "", false
	}
	return readinessPathOf(c)
}

// readinessPathOf is the path that says the public part is ready: the one
// it declares, or, for a static part the forge serves from its own file
// server, "/" — that server has no start-up, no auth and no HTTPS redirect,
// so its root answering through both doors is the readiness of the site,
// observed rather than assumed. A built or pushed part that declares none
// has not said when it is ready.
func readinessPathOf(c *appcontract.Contract) (string, bool) {
	for i := range c.Parts {
		p := &c.Parts[i]
		if !p.Public {
			continue
		}
		if p.Intent != nil && p.Intent.Readiness != nil && p.Intent.Readiness.HTTP != "" {
			return p.Intent.Readiness.HTTP, true
		}
		if p.IsStatic() {
			return "/", true
		}
	}
	return "", false
}

// Observe is the production runtime evaluator: resolve the
// standing deployment, resolve the path, probe both doors. source is the
// commit the review recorded; a probe of the contract judges that commit
// ("" is the default branch as it stands).
func (o Runtime) Observe(ctx context.Context, projectID ids.ID, source, probe string) (state conformance.Outcome, detail string) {
	ps, err := check.ParseProbe(probe)
	if err != nil {
		return conformance.EvaluationError, err.Error()
	}
	if ps.MemoryDeclared {
		// The reviewed commit's contract, deployed or not.
		p := projectOrEmpty(ctx, o.db, projectID)
		rev := source
		if rev == "" {
			rev = p.DefaultBranch
		}
		v := o.memoryDeclared(ctx, Target{ProjectID: projectID, Slug: p.Slug, Revision: rev})
		return outcomeOf(v.Outcome), v.Detail
	}
	t, _, why, ok := o.standingTarget(ctx, projectID)
	if !ok {
		// A deployment the forge cannot run because the app declares no
		// contract is the app's gap, not the forge's inability.
		if ps.Readiness && t.Slug != "" && t.Container != "" {
			if _, err := docker.ReadWorkspaceContract(o.repo(t.Slug)); err == docker.ErrNoContract {
				return conformance.DoesNotConform, undeclaredReadiness
			}
		}
		return conformance.CannotAssess, why
	}
	if ps.BrowserEnv {
		v := o.judgeBrowser(ctx, t)
		return outcomeOf(v.Outcome), v.Detail
	}
	if ps.Page {
		state, detail := check.JudgePage(ctx, t.FrontDoor)
		return outcomeOf(state), detail
	}
	if ps.Standing() {
		v := o.memoryWithin(ctx, t, ps.MemoryWithin)
		return outcomeOf(v.Outcome), v.Detail
	}
	if ps.Readiness {
		path, declared := o.declaredReadinessPath(ctx, t)
		if !declared && ctx.Err() != nil {
			return conformance.CannotAssess, readinessReadStopped
		}
		if !declared {
			return conformance.DoesNotConform, undeclaredReadiness
		}
		ps.Path = path
	}
	return bothDoors(ctx, t, ps)
}

// undeclaredReadiness is the finding when the forge serves an app that
// says nothing about when it is ready. "Cannot assess" is for the forge's
// own inability (the app not running when the review looked); a served
// app with no readiness declared has not met the way of working. Read as
// "cannot assess", such projects would sit there indefinitely while the
// shell check passed them for having no .apptown/app.yaml at all.
const undeclaredReadiness = "AppTown serves this app but nothing declares when it is ready: .apptown/app.yaml names no health.readiness.http for the public part (a retired compose.yaml or app.json does not count), so AppTown falls back to probing GET /. Declare the contract in .apptown/app.yaml with  health: { readiness: { http: /healthz } }  under the served part, and give the app a /healthz that answers 200 without touching the database"

// readinessReadStopped is a readiness check whose contract read was cut
// short: a stopped read says nothing about what the app declares, so it is
// never the undeclared-readiness finding.
const readinessReadStopped = "the check was stopped before the app's contract could be read"

// Probe judges one probe spec against an already-resolved
// target (a change's preview at the gate; the standing deployment in a
// review goes through Observe). It answers in the judge's own
// words, with whether an unknown answer is the forge's own fault: the gate
// records an evaluation, not a stored verdict.
func (o Runtime) Probe(ctx context.Context, t Target, probe string) check.ProbeVerdict {
	ps, err := check.ParseProbe(probe)
	if err != nil {
		return check.ProbeVerdict{Outcome: check.Error, Detail: err.Error()}
	}
	if ps.MemoryDeclared {
		return o.memoryDeclared(ctx, t)
	}
	if ps.Standing() {
		return o.memoryWithin(ctx, t, ps.MemoryWithin)
	}
	if ps.BrowserEnv {
		return o.judgeBrowser(ctx, t)
	}
	if ps.Page {
		state, detail := check.JudgePage(ctx, t.FrontDoor)
		return check.ProbeVerdict{Outcome: state, Detail: detail}
	}
	if ps.Readiness {
		path, declared := o.declaredReadinessPathAt(ctx, t)
		if !declared && ctx.Err() != nil {
			return check.ProbeVerdict{Outcome: check.CannotAssess, Detail: readinessReadStopped, Fault: true}
		}
		if !declared {
			return check.ProbeVerdict{Outcome: check.DoesNotConform, Detail: undeclaredReadiness}
		}
		ps.Path = path
	}
	state, detail := check.ProbeBothDoors(ctx, check.HTTPProber, t.FrontDoor, t.Naked, ps)
	return check.ProbeVerdict{Outcome: state, Detail: detail}
}

// bothDoors judges one probe spec against a resolved target's two addresses,
// and says the answer in conformance's vocabulary.
func bothDoors(ctx context.Context, t Target, ps check.ProbeSpec) (conformance.Outcome, string) {
	state, detail := check.ProbeBothDoors(ctx, check.HTTPProber, t.FrontDoor, t.Naked, ps)
	return outcomeOf(state), detail
}

// outcomeOf carries a judge's outcome into conformance's, which also holds the
// states only a person can reach (excepted, not applicable).
func outcomeOf(o check.Outcome) conformance.Outcome {
	state, err := conformance.ParseOutcome(o.String())
	if err != nil {
		return conformance.EvaluationError
	}
	return state
}

// declaredReadinessPathAt reads the readiness path from the target's own
// tree when it names one (a change may have moved it), else from the
// project as the standing deployment resolver does.
func (o Runtime) declaredReadinessPathAt(ctx context.Context, t Target) (string, bool) {
	var c *appcontract.Contract
	switch {
	case t.Recorded != nil:
		c = t.Recorded.Contract
	case t.ContractDir == "":
		return o.declaredReadinessPath(ctx, t)
	default:
		var err error
		if c, err = docker.ReadWorkspaceContract(t.ContractDir); err != nil {
			return "", false
		}
	}
	if c == nil {
		return "", false
	}
	return readinessPathOf(c)
}
