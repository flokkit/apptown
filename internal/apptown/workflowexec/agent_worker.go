package workflowexec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/agentenv"
	piharness "github.com/flokkit/apptown/internal/apptown/agentharness/pi"
	"github.com/flokkit/apptown/internal/apptown/dependency"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/console"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/strs"
)

// Agent steps on a worker (design/operations/workers.md, "Agent and review
// steps on the worker"). An invocation's request is prepared on AppTown as bytes, from
// what was admitted: the step's recorded configuration, the run's
// definition and adoption, the attempt's source commit in the project's
// hub, the attachment store and the worker's own agent image. Nothing is
// written into or read from the run's folder, so nothing in it (a .apptown
// that is a link) can choose what is sent or where anything is written.
// A step that needs what the worker cannot provide yet is refused before
// any effect (WorkerRefusal), and never falls back to AppTown's host.

// AgentTaskDriver is a worker's task driver, which also reports the agent
// image it runs agent steps in and the result files a task left
// (execution.WorkerTasks).
type AgentTaskDriver interface {
	execution.TaskDriver
	AgentImage(context.Context) (execution.AgentImage, error)
	Results(context.Context, execution.TaskRef) (map[string]execution.ResultFile, error)
	Profile(context.Context) (execution.WorkerProfile, error)
}

// AgentWorker is where agent steps run when AppTown has a worker: the
// task runner its script tasks use, whose driver is the worker's
// (AgentTaskDriver) and whose profile admits the steps' invocations.
type AgentWorker struct {
	Tasks *TaskRunner
}

// driver is the worker's driver, as an agent step needs it.
func (w *AgentWorker) driver() (AgentTaskDriver, error) {
	if w == nil || w.Tasks == nil {
		return nil, errors.New("this AppTown has no worker for agent steps")
	}
	d, ok := w.Tasks.Driver.(AgentTaskDriver)
	if !ok {
		return nil, fmt.Errorf("AppTown's task backend (%s) cannot run agent steps: it reports no agent image or result files", w.Tasks.backend())
	}
	return d, nil
}

// WorkerRefusal: the step needs what the worker cannot provide yet, or what
// it needs could not be read. It is refused before any effect.
type WorkerRefusal struct{ Reason string }

func (r *WorkerRefusal) Error() string { return r.Reason }

// needsWhatTheWorkerLacks refuses a step for something it needs.
func needsWhatTheWorkerLacks(what string) error {
	return &WorkerRefusal{Reason: "this step needs " + what + ", which AppTown's worker cannot provide yet; it is not run on AppTown's own machine instead"}
}

// Where an agent on a worker finds what AppTown sends it (read-only, under
// execution.InputsRoot) and keeps what Pi writes for itself (in the
// attempt's workspace state, beside the tree, never in it).
const (
	workerSteeringFile = execution.InputsRoot + "/steering.md"
	workerClaimFile    = execution.InputsRoot + "/review-claim.md"
	workerHomeIn       = execution.InputsRoot + "/pi-home"
	workerCLIFile      = execution.InputsRoot + "/bin/apptown"
	workerCLIDir       = execution.InputsRoot + "/bin"
	workerSessionsIn   = execution.InputsRoot + "/sessions"
	workerHome         = execution.StateRoot + "/home"
	workerSessions     = execution.StateRoot + "/sessions"
	workerTree         = "/work"
	workerCarriedIn    = execution.InputsRoot + "/carried"
)

var workerSteering = steeringPaths{Resources: execution.InputsRoot + "/resources", Attachments: execution.InputsRoot + "/attachments", Sessions: workerSessions}

// Declared result files' bounds.
const (
	maxTranscriptResult = 32 << 20
	maxOutputResult     = 1 << 20
	maxReviewReport     = 256 << 10
	// maxEventsResult bounds an invocation's event stream, within what a
	// request's results may hold together (execution.MaxResultTotalBytes).
	maxEventsResult = 62 << 20
)

// workerReviewReport is where a reviewer on a worker writes its verdict.
const workerReviewReport = workerTree + "/.apptown/review.json"

// workerAgentCall is one invocation of Pi for an agent step attempt on the
// worker.
type workerAgentCall struct {
	run    *workflow.Run
	step   *workflow.Step // claimed: its Attempt is the attempt's
	source workflow.Source
	// commit is the attempt's source commit, in the project's hub: the
	// workspace is made from it, and the app's needs are read from it.
	commit string
	// invocation names this run of Pi within the attempt ("agent",
	// "agent-retry").
	invocation string
	// at is when this invocation is admitted. sessionAt is when the
	// attempt's first invocation was, which names the transcript file, so
	// every invocation of the attempt names the same file; zero is at.
	at, sessionAt time.Time
	// session is Pi's session id. transcript is a continued session's
	// stored transcript, sent as it is; nil starts a new session.
	session    string
	transcript []byte
	// review is a review step's brief and configuration; nil for an agent
	// step. A large claim is sent as a file, and the request's claim says
	// where.
	review *reviewCall
}

// reviewCall is what a review invocation is built from.
type reviewCall struct {
	cfg   reviewConfig
	brief reviewBrief
}

// workerAgentRequest is an invocation's admitted request, and what the
// AppTown records about it.
type workerAgentRequest struct {
	spec                           execution.TaskSpec
	steering                       steeringFile
	image                          execution.AgentImage
	model, modelSlot, modelProfile string
	// sessionFile is the transcript's name in the sessions folder.
	sessionFile string
	// claim is what a review's brief carries for its claim.
	claim string
	// privateDocker is what the invocation's evidence says of its private
	// Docker; "" for none. profile is the worker's, as it reported it.
	privateDocker string
	profile       *execution.WorkerProfile
}

// privateLimits are an invocation's limits with private Docker: its
// daemon has the same, and zero is never unlimited there, so a limit the
// worker's profile leaves at zero is this.
func privateLimits(l execution.Limits) execution.Limits {
	if l.CPUs <= 0 {
		l.CPUs = 2
	}
	if l.MemoryBytes <= 0 {
		l.MemoryBytes = 4 << 30
	}
	if l.PIDs <= 0 {
		l.PIDs = 2048
	}
	return l
}

// prepareAgentOnWorker refuses a step the worker cannot run, then reads the
// worker's agent image and builds the invocation's request. Nothing is asked
// of the worker but its image, and only once the step's own requirements
// are known to be met.
func (a *Executor) prepareAgentOnWorker(ctx context.Context, c workerAgentCall) (workerAgentRequest, error) {
	driver, err := a.worker.driver()
	if err != nil {
		return workerAgentRequest{}, err
	}
	cfg, k, err := a.admitOnWorker(ctx, c)
	if err != nil {
		return workerAgentRequest{}, err
	}
	private, needEnv, err := a.privateOnWorker(ctx, c, &cfg, k)
	if err != nil {
		return workerAgentRequest{}, err
	}
	if private != nil {
		// What its private Docker rests on, as the worker measured it: the
		// worker refuses the request if that has changed when it makes it.
		profile, err := driver.Profile(ctx)
		if err != nil {
			return workerAgentRequest{}, fmt.Errorf("reading the worker's profile: %w", err)
		}
		private.Profile = &profile
	}
	image, err := driver.AgentImage(ctx)
	if err != nil {
		return workerAgentRequest{}, fmt.Errorf("reading the worker's agent image: %w", err)
	}
	req, err := a.buildAgentOnWorker(ctx, c, &cfg, k, image, private, needEnv)
	if err == nil && private != nil {
		req.privateDocker, req.profile = privateDockerQualification(*private.Profile), private.Profile
	}
	return req, err
}

// privateDockerQualification is what an invocation with private Docker on a
// worker records of what it rests on (design/operations/workers.md,
// "Private Docker on the worker"): the qualification record of the profile
// the worker's measured facts match, or "unqualified" when they match none.
func privateDockerQualification(p execution.WorkerProfile) string {
	if record := p.Qualification(); record != "" {
		return "qualified: " + p.Name + " (" + record + ")"
	}
	return privateDockerUnqualified
}

// privateDockerUnqualified is what an invocation records when its worker's
// measured profile matches no qualified one.
const privateDockerUnqualified = "unqualified: private Docker on a worker whose measured profile matches no qualified one (internal/execution/qualified_profiles.json)"

// privateDockerOptIn is why a step declaring private Docker is refused on a
// worker without the operator's opt-in.
const privateDockerOptIn = "private Docker on a worker is the operator's opt-in: start AppTown with -private-docker"

// privateOnWorker is what an agent step has beside it on the worker, frozen
// now: the services it declares, and those fulfilling the app's needs as
// the admitted commit declares them (with their parameters) and the
// catalog resolves them, with the environment reaching them; and its
// private daemon when it declares docker: true. Nil when it has none. A
// commit whose declaration cannot be read refuses the step before anything
// is made. A declaration that does not parse, or whose needs the catalogue
// refuses or does not hold, starts no need services: the agent is told why
// (cfg.needsRefused), since fixing the declaration may be its change.
func (a *Executor) privateOnWorker(ctx context.Context, c workerAgentCall, cfg *agentConfig, k agentKnobs) (*execution.PrivateDocker, []string, error) {
	if c.review != nil {
		return nil, nil, nil
	}
	services := slices.Clone(cfg.Services)
	var env []string
	if cfg.Needs {
		raw, err := appContractAt(ctx, c.source.Repository(a.cfg.DataDir), c.commit)
		if err != nil {
			return nil, nil, &WorkerRefusal{Reason: "the app's declared needs could not be read from commit " + c.commit + ": " + err.Error()}
		}
		var contract *appcontract.Contract
		if raw != nil {
			if contract, err = appcontract.Parse(raw); err != nil {
				cfg.needsRefused = ".apptown/app.yaml does not parse: " + err.Error()
			}
		}
		if contract != nil && len(contract.Needs) > 0 {
			needs, needEnv, err := (dependency.LocalRuntime{Catalog: a.dependencies, Steps: true}).Fulfil(ctx, contract.Needs, contract.NeedParams)
			if why, ok := needsRefusal(err); ok {
				cfg.needsRefused = why
			} else if err != nil {
				return nil, nil, err
			} else {
				services = append(services, needs...)
				for key, v := range needEnv {
					env = append(env, key+"="+v)
				}
				slices.Sort(env)
			}
		}
	}
	if !k.useDind && len(services) == 0 {
		return nil, nil, nil
	}
	if k.useDind && !a.cfg.PrivateDockerOnWorker {
		return nil, nil, &WorkerRefusal{Reason: "this step declares private Docker (docker: true): " + privateDockerOptIn}
	}
	p := &execution.PrivateDocker{Daemon: k.useDind, Services: services}
	if k.useDind {
		mirror, err := a.workshop.Mirror(ctx)
		if err != nil {
			return nil, nil, err
		}
		p.Mirror = mirror
	}
	if err := p.Valid(privateLimits(a.worker.Tasks.Profile.Limits)); err != nil {
		return nil, nil, &WorkerRefusal{Reason: "the step's private Docker: " + err.Error()}
	}
	return p, env, nil
}

// admitOnWorker reads what the step needs from what was admitted and
// refuses what the worker cannot provide (WorkerRefusal). It has no effect
// and asks the worker nothing.
func (a *Executor) admitOnWorker(ctx context.Context, c workerAgentCall) (agentConfig, agentKnobs, error) {
	var cfg agentConfig
	if err := json.Unmarshal(c.step.Config, &cfg); err != nil {
		return cfg, agentKnobs{}, &WorkerRefusal{Reason: "the step's recorded configuration could not be read: " + err.Error()}
	}
	// resolveAgentKnobs takes an unreadable definition as declaring nothing;
	// what a step needs is never read that way here.
	if _, err := c.run.Definition(); err != nil {
		return cfg, agentKnobs{}, &WorkerRefusal{Reason: "the run's recorded definition could not be read: " + err.Error()}
	}
	k := a.resolveAgentKnobs(c.run, &cfg)
	if c.review != nil {
		// A review's knobs are its own, as on AppTown's machine: its
		// model and thinking, AppTown's agent image, in Docker.
		k = agentKnobs{model: c.review.cfg.Model, thinking: strs.FirstNonBlank(c.review.cfg.Thinking, "medium"), executor: execution.Docker, image: a.cfg.Image}
	}
	return cfg, k, a.refuseOnWorker(c, &cfg, k)
}

// refuseOnWorker refuses what the worker cannot provide yet, from what was
// admitted: the step's configuration and the run's definition.
func (a *Executor) refuseOnWorker(c workerAgentCall, cfg *agentConfig, k agentKnobs) error {
	switch {
	case !c.source.RepoBacked():
		return &WorkerRefusal{Reason: "this step's project is not repository-backed, and AppTown's worker works only from a repository's commit; it is not run on AppTown's own machine instead"}
	case k.executor != execution.Docker:
		return needsWhatTheWorkerLacks(fmt.Sprintf("the %q executor", k.executor))
	case k.image != a.cfg.Image:
		return needsWhatTheWorkerLacks(fmt.Sprintf("its own agent image %q", k.image))
	case string(cfg.Scm) != "":
		return needsWhatTheWorkerLacks("source-control access (scm: " + string(cfg.Scm) + ")")
	}
	return nil
}

var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// maxContractBytes bounds the app declaration read from a commit.
const maxContractBytes = 1 << 20

// appContractAt is the app's declaration (.apptown/app.yaml) at commit, in
// the project's hub, unparsed; nil when there is none. One that is not a
// regular file, or cannot be read, is an error, never "nothing".
func appContractAt(ctx context.Context, repo, commit string) ([]byte, error) {
	if !commitPattern.MatchString(commit) {
		return nil, fmt.Errorf("%q is not a commit", commit)
	}
	entry, err := gitexec.HubOutput(ctx, repo, nil, "ls-tree", "-z", "--full-tree", commit, "--", ".apptown/app.yaml")
	if err != nil {
		return nil, fmt.Errorf("listing it: %w", err)
	}
	if len(entry) == 0 {
		return nil, nil
	}
	meta, _, ok := bytes.Cut(bytes.TrimSuffix(entry, []byte{0}), []byte("\t"))
	fields := strings.Fields(string(meta))
	if !ok || len(fields) != 3 || fields[1] != "blob" || (fields[0] != "100644" && fields[0] != "100755") {
		return nil, fmt.Errorf(".apptown/app.yaml is not a regular file (%s)", meta)
	}
	raw, err := gitexec.HubOutput(ctx, repo, nil, "cat-file", "blob", fields[2])
	if err != nil {
		return nil, fmt.Errorf("reading it: %w", err)
	}
	if len(raw) > maxContractBytes {
		return nil, fmt.Errorf(".apptown/app.yaml is %d bytes, over %d", len(raw), maxContractBytes)
	}
	return raw, nil
}

var (
	sessionIDPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	invocationPattern = regexp.MustCompile(`^[a-z][a-z-]{0,31}$`)
)

// buildAgentOnWorker builds the invocation's request from what was
// admitted and the worker's agent image.
func (a *Executor) buildAgentOnWorker(ctx context.Context, c workerAgentCall, cfg *agentConfig, k agentKnobs, image execution.AgentImage, private *execution.PrivateDocker, needEnv []string) (workerAgentRequest, error) {
	run, step := c.run, c.step
	if step.Attempt <= 0 || !invocationPattern.MatchString(c.invocation) {
		return workerAgentRequest{}, fmt.Errorf("an invocation needs its claimed attempt and a name (attempt %d, %q)", step.Attempt, c.invocation)
	}
	if image.Name == "" || len(image.Labels) == 0 {
		return workerAgentRequest{}, &WorkerRefusal{Reason: "the worker reported no labels for its agent image, so what this step is promised (subagents, extensions) cannot be checked against it"}
	}
	labels := piharness.MapLabels(image.Labels)
	home, err := a.home.SentHome(ctx, run.ProjectID, labels)
	if err != nil {
		return workerAgentRequest{}, fmt.Errorf("preparing pi's home: %w", err)
	}
	if len(home.Missing) > 0 && c.review == nil { // a review loads no extensions
		return workerAgentRequest{}, needsWhatTheWorkerLacks("the extensions this project enabled (" + strings.Join(home.Missing, ", ") + "), which the worker's agent image does not carry")
	}
	if !sessionIDPattern.MatchString(c.session) {
		return workerAgentRequest{}, fmt.Errorf("invalid session id %q", c.session)
	}
	out := workerAgentRequest{image: image}
	out.model, out.modelSlot, out.modelProfile = a.modelSelection.Resolve(ctx, run.ProjectID, k.model)

	var inputs []execution.Input
	add := func(at string, content []byte, mode fs.FileMode) error {
		in, err := execution.NewInput(at, content, mode)
		if err == nil {
			inputs = append(inputs, in)
		}
		return err
	}

	// A review is not steered: it is given its brief, and only a large
	// claim as a file.
	var st steeringFile
	claim := ""
	if c.review == nil {
		attachments, err := a.attachmentsFor(run.ChangeID)
		if err != nil {
			return workerAgentRequest{}, err
		}
		names := make([]string, len(attachments))
		for i, f := range attachments {
			names[i] = f.Path
			if err := add(workerSteering.Attachments+"/"+f.Path, f.Body, 0o644); err != nil {
				return workerAgentRequest{}, err
			}
		}
		var resources []steeringResource
		if st, resources, err = a.composeSteering(ctx, run, cfg, k, workerSteering, names, piharness.DelegationWith(labels)); err != nil {
			return workerAgentRequest{}, err
		}
		if st.Text != "" {
			if err := add(workerSteeringFile, []byte(st.Text), 0o644); err != nil {
				return workerAgentRequest{}, err
			}
		}
		for _, r := range resources {
			if err := add(r.Path, r.Body, 0o644); err != nil {
				return workerAgentRequest{}, err
			}
		}
	} else {
		claim = c.review.brief.claim
	}
	out.steering = st
	out.claim = claim
	if staged, pointer := stagedClaim(claim, workerClaimFile); staged != nil {
		if err := add(workerClaimFile, staged, 0o644); err != nil {
			return workerAgentRequest{}, err
		}
		out.claim = pointer
	}
	for _, f := range home.Files {
		if err := add(workerHomeIn+"/"+f.Path, f.Content, 0o644); err != nil {
			return workerAgentRequest{}, err
		}
	}
	carried, err := a.earlierOutputs(ctx, run, c.source, step)
	if err != nil {
		return workerAgentRequest{}, err
	}
	var earlier *wroteFile
	if c.review == nil {
		if earlier, err = a.ownEarlierReport(ctx, run, c.source, step, cfg); err != nil {
			return workerAgentRequest{}, err
		}
		if earlier != nil {
			carried = append(carried, *earlier)
		}
	}
	for _, f := range carried {
		if err := add(workerCarriedIn+"/"+f.rel, []byte(f.body), 0o644); err != nil {
			return workerAgentRequest{}, err
		}
	}
	cli, err := a.agentCLI()
	if err != nil {
		return workerAgentRequest{}, err
	}
	if err := add(workerCLIFile, cli, 0o755); err != nil {
		return workerAgentRequest{}, err
	}

	// The transcript file is named by AppTown, so its path is known
	// before Pi starts and is a declared result. Pi is pointed at
	// exactly that file (--session), never left to find the session by id:
	// another file in the folder naming the same session is not one it
	// reads or writes, so the captured file is the session's history.
	sessionAt := c.sessionAt
	if sessionAt.IsZero() {
		sessionAt = c.at
	}
	out.sessionFile = piharness.SessionFileName(c.session, sessionAt)
	transcript := c.transcript
	if transcript == nil {
		transcript = piharness.SessionHeader(c.session, sessionAt, workerTree)
	} else if id, err := piharness.SessionHeaderID(transcript); err != nil || id != c.session {
		return workerAgentRequest{}, fmt.Errorf("the earlier session's history is not session %s's", c.session)
	}
	if err := add(workerSessionsIn+"/"+out.sessionFile, transcript, 0o644); err != nil {
		return workerAgentRequest{}, err
	}

	bound, err := integration.RegistryBindings(ctx, a.db)
	if err != nil {
		return workerAgentRequest{}, fmt.Errorf("reading the registry bindings: %w", err)
	}
	workspace := agentWorkspace(step.ID, step.Attempt)
	amb := agentenv.AmbientFor(bound, workspace)

	sessionFile := workerSessions + "/" + out.sessionFile
	var args []string
	if c.review != nil {
		brief := c.review.brief
		brief.claim = out.claim
		args = piharness.SessionReviewArgs(sessionFile, out.model, k.thinking, brief.prompt())
	} else {
		prompt := a.agentPrompt(ctx, run, cfg, c.source, earlier)
		args = piharness.StepArgs(piharness.StepPaths{SessionFile: sessionFile, Steering: workerSteeringFile}, c.session, k.executor, st.Text != "", out.model, k.thinking, prompt)
	}
	command := console.ToolchainArgs(append([]string{"pi"}, args...))
	// pi's events are written to a file in the attempt's state as they go,
	// and read from there when it exits (a declared result), never from
	// its log: the log's end can be lost (piharness.CaptureEvents).
	events := execution.StateRoot + "/events-" + c.invocation + ".jsonl"
	argv := append([]string{"sh", "-c", workerEntry(out.sessionFile, home.Link, carried, events), "sh"}, command...)

	stepEnv, err := a.stepEnv(ctx, run.ProjectID)
	if err != nil {
		return workerAgentRequest{}, err
	}
	env := append([]string{}, stepEnv...)
	env = append(env, a.apptownEnv(ctx, run, step)...)
	if !c.source.ChangeID.IsZero() {
		// The one ref this attempt's token may push: `apptown check` sends the
		// agent's HEAD there to have it checked, as feedback only, before
		// the step's work is collected (scm.ScratchRef).
		env = append(env, "APPTOWN_SCRATCH_REF="+scm.ScratchRef(c.source.ChangeID, step.ID, step.Attempt))
	}
	env = append(env, amb.Env...)
	if k.useDind {
		// As on AppTown's host: what the app's containers get from the
		// project's integrations. No registry credential travels.
		given, err := a.integrations.Environment(ctx, run.ProjectID)
		if err != nil {
			return workerAgentRequest{}, err
		}
		env = append(env, given...)
	}
	env = append(env, needEnv...)
	// The agent's own repository at /work/.git (execution.AgentGit) is
	// owned by the worker's user, which need not be the image's.
	env = append(env, "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=safe.directory", "GIT_CONFIG_VALUE_0="+workerTree)

	results := []execution.Result{{Name: "transcript", Path: sessionFile, Max: maxTranscriptResult}, {Name: "events", Path: events, Max: maxEventsResult}}
	if cfg.Output.File != "" && c.review == nil {
		at := path.Join(workerTree, cfg.Output.File)
		if !strings.HasPrefix(at, workerTree+"/") || path.IsAbs(cfg.Output.File) {
			return workerAgentRequest{}, fmt.Errorf("the step's output file %q is not in the tree", cfg.Output.File)
		}
		results = append(results, execution.Result{Name: "output", Path: at, Max: maxOutputResult})
	}
	timeout := agentTimeout
	if c.review != nil {
		// A report left in the tree before the review started never counts.
		results = append(results, execution.Result{Name: "review", Path: workerReviewReport, Max: maxReviewReport, Fresh: true})
		timeout = reviewTimeout
	}
	if cfg.TimeoutMinutes > 0 {
		timeout = time.Duration(cfg.TimeoutMinutes) * time.Minute
	}
	limits := a.worker.Tasks.Profile.Limits
	if private != nil {
		limits = privateLimits(limits)
	}
	limits.WallClock = timeout
	repo := c.source.Repository(a.cfg.DataDir)
	out.spec = execution.TaskSpec{
		Key:             agentOperation(step.ID, step.Attempt, c.invocation),
		Admission:       execution.Admission{Profile: a.worker.Tasks.Profile.Name, Limits: limits, Network: execution.NetworkDefault, Expires: c.at.Add(timeout + taskGrace)},
		Image:           image.Name,
		Argv:            argv,
		Env:             env,
		Source:          &execution.TaskSource{Repository: repo, Commit: c.commit},
		SharedWorkspace: workspace,
		AgentGit:        agentGit(ctx, c.source, c.commit, repo),
		Files:           amb.Files,
		Inputs:          inputs,
		Results:         results,
		Private:         private,
	}
	if err := out.spec.Validate(c.at); err != nil {
		return workerAgentRequest{}, fmt.Errorf("the step's request: %w", err)
	}
	return out, nil
}

// attachmentsFor reads the change's attachments from AppTown's store
// (the upload path's), never from a run's folder. Each is a regular file
// in the store; no store is no attachments.
func (a *Executor) attachmentsFor(changeID ids.ID) ([]steeringResource, error) {
	if changeID.IsZero() {
		return nil, nil
	}
	dir := (workflow.GitWorkspace{DataDir: a.cfg.DataDir}).AttachmentDir(changeID)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading the change's attachments: %w", err)
	}
	var out []steeringResource
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !e.Type().IsRegular() {
			return nil, fmt.Errorf("the change's attachment %s is not a regular file", e.Name())
		}
		body, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("reading the change's attachment %s: %w", e.Name(), err)
		}
		out = append(out, steeringResource{Path: e.Name(), Body: body})
	}
	return out, nil
}

// agentCLI is the packaged apptown CLI agents run, as bytes. An agent on a
// worker is always sent it: its steering tells it to run apptown check.
func (a *Executor) agentCLI() ([]byte, error) {
	at, err := a.cli.Path()
	if err != nil {
		return nil, fmt.Errorf("the agents' CLI (apptown-linux) is not available: %w", err)
	}
	cli, err := os.ReadFile(at)
	if err != nil {
		return nil, fmt.Errorf("reading the agents' CLI (apptown-linux): %w", err)
	}
	return cli, nil
}

// workerEntry is the shell an invocation starts in, before Pi: Pi's home
// is copied from the inputs into the workspace's state the first time, with
// the link to the image's baked packages; the transcript file AppTown
// named is placed in the sessions folder when it is not there yet (a later
// invocation of the attempt appends to the same file); HOME is the state's
// home and AppTown CLI is first on PATH. Each copy is made beside its
// place and moved there, so an interrupted copy is never taken for a whole
// one. The names are AppTown's own (a session file name and a fixed link),
// quoted besides. Pi then runs with its events written to events
// (piharness.CaptureEvents), and the entry exits with Pi's status.
func workerEntry(sessionFile string, link piharness.SentLink, carried []wroteFile, events string) string {
	var b strings.Builder
	b.WriteString("set -e\n")
	// Earlier steps' outputs are put in the tree where the run's folder
	// would hold them, unless the tree already has something there: the
	// source's own file, or what an earlier invocation of this attempt left.
	for _, f := range carried {
		at := shellQuote(workerTree + "/" + f.rel)
		b.WriteString("if [ ! -e " + at + " ] && [ ! -L " + at + " ]; then\n")
		b.WriteString("  mkdir -p " + shellQuote(path.Dir(workerTree+"/"+f.rel)) + "\n")
		b.WriteString("  cp " + shellQuote(workerCarriedIn+"/"+f.rel) + " " + at + "\n")
		b.WriteString("fi\n")
	}
	b.WriteString("if [ ! -e " + workerHome + " ]; then\n")
	b.WriteString("  rm -rf " + workerHome + ".part\n")
	b.WriteString("  cp -R " + workerHomeIn + " " + workerHome + ".part\n")
	if link != (piharness.SentLink{}) {
		b.WriteString("  ln -s " + shellQuote(link.Target) + " " + shellQuote(workerHome+".part/"+link.Path) + "\n")
	}
	b.WriteString("  mv " + workerHome + ".part " + workerHome + "\n")
	b.WriteString("fi\n")
	b.WriteString("mkdir -p " + workerSessions + "\n")
	session := shellQuote(workerSessions + "/" + sessionFile)
	b.WriteString("if [ ! -e " + session + " ]; then\n")
	b.WriteString("  cp " + shellQuote(workerSessionsIn+"/"+sessionFile) + " " + execution.StateRoot + "/session.part\n")
	b.WriteString("  mv " + execution.StateRoot + "/session.part " + session + "\n")
	b.WriteString("fi\n")
	b.WriteString("export HOME=" + workerHome + "\n")
	b.WriteString("export PATH=\"" + workerCLIDir + ":$PATH\"\n")
	b.WriteString(piharness.CaptureEvents(events, events+".status"))
	return b.String()
}

// agentGit is the repository an agent or review is given at /work/.git:
// the source's history on the change's branch, with the base branch at the
// newest commit of the base the source contains: where the change started,
// or the later main it has since caught up with, so a rework after a
// catch-up compares its work with that main and not the old one. The base
// is left out when the change's start is not in the source's history (a
// branch rebuilt on another base); the agent then has no base branch to
// compare with.
func agentGit(ctx context.Context, source workflow.Source, commit, repo string) *execution.AgentGit {
	out := &execution.AgentGit{Branch: strs.FirstNonBlank(source.Branch, "work")}
	base := source.DefaultBranch
	if base == "" || base == out.Branch || source.ChangeBaseSHA == "" || !gitexec.HubAncestor(ctx, repo, source.ChangeBaseSHA, commit) {
		return out
	}
	out.Base, out.BaseCommit = base, source.ChangeBaseSHA
	if tip := gitexec.HubRef(ctx, repo, "refs/heads/"+base); tip != "" {
		raw, err := gitexec.HubOutput(ctx, repo, nil, "merge-base", "--end-of-options", tip, commit)
		if met := strings.TrimSpace(string(raw)); err == nil && commitPattern.MatchString(met) {
			out.BaseCommit = met
		}
	}
	return out
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// earlierOutputs are the declared output files of the run's other steps, as
// their passing outcomes recorded them, at the paths they declared. In the
// run's folder on AppTown they stay where the earlier step wrote them
// (.apptown/PLAN.md for the step that carries out the plan); a worker starts
// each attempt from committed files, so it is sent them. Another step's
// output at this step's own path is never sent: it must not pass for this
// attempt's; this step's own report from its earlier attempt is sent beside
// them (ownEarlierReport). The latest step to declare a path wins.
func (a *Executor) earlierOutputs(ctx context.Context, run *workflow.Run, source workflow.Source, step *workflow.Step) ([]wroteFile, error) {
	runs := []ids.ID{run.ID}
	if !source.RootRunID.IsZero() && source.RootRunID != run.ID {
		runs = []ids.ID{source.RootRunID, run.ID}
	}
	var own agentConfig
	if err := workflow.DecodeStepConfig(step.Config, &own); err != nil {
		return nil, err
	}
	at := map[string]int{}
	var out []wroteFile
	for _, id := range runs {
		ref := workflow.RunRef{ID: id, ProjectID: run.ProjectID}
		steps, err := workflow.ReadScopedStepsIn(ctx, a.db, ref)
		if err != nil {
			return nil, fmt.Errorf("reading the run's steps: %w", err)
		}
		for _, s := range steps {
			var cfg agentConfig
			if s.ID == step.ID || s.Type != AgentKind || json.Unmarshal(s.Config, &cfg) != nil || cfg.Output.File == "" || cfg.Output.Kind == "" {
				continue
			}
			rel, ok := outputPath(cfg.Output.File)
			if !ok || (own.Output.File != "" && rel == path.Clean(own.Output.File)) {
				continue
			}
			name := strs.FirstNonBlank(cfg.Output.Name, s.Name)
			got, err := workflow.ReadRunArtifactsIn(ctx, a.db, ref, workflow.ArtifactSelection{Kinds: []string{cfg.Output.Kind}, Name: name, Latest: true})
			if err != nil {
				return nil, fmt.Errorf("reading %s's output: %w", s.Name, err)
			}
			if len(got) == 0 || got[0].Body == "" {
				continue
			}
			if i, ok := at[rel]; ok {
				out[i].body = got[0].Body
				continue
			}
			at[rel] = len(out)
			out = append(out, wroteFile{rel, got[0].Body})
		}
	}
	return out, nil
}

// outputPath is a declared output file as a path in the tree; false when it
// names no file there.
func outputPath(file string) (string, bool) {
	rel := path.Clean(file)
	return rel, !path.IsAbs(rel) && rel != "." && !strings.HasPrefix(rel, "../")
}

// ownEarlierReport is this step's own report from an earlier attempt in its
// run (a machine or a person's rework, a retry), as that attempt recorded
// it, at its declared path; nil when there is none, and on a restart, whose
// work goes back to the base. The report describes the whole change, so a
// rework puts it back in the tree and the agent updates it: an agent that
// started a rework without it would write only the rework's note, and the
// unclaimed-file check would list every file of the change.
func (a *Executor) ownEarlierReport(ctx context.Context, run *workflow.Run, source workflow.Source, step *workflow.Step, cfg *agentConfig) (*wroteFile, error) {
	rel, ok := outputPath(cfg.Output.File)
	if cfg.Output.File == "" || cfg.Output.Kind == "" || !ok || cfg.RetryMode == workflow.RetryRestart.String() {
		return nil, nil
	}
	ref := workflow.RunRef{ID: run.ID, ProjectID: run.ProjectID}
	got, err := workflow.ReadRunArtifactsIn(ctx, a.db, ref, workflow.ArtifactSelection{Kinds: []string{cfg.Output.Kind}, Name: strs.FirstNonBlank(cfg.Output.Name, step.Name), Latest: true})
	if err != nil {
		return nil, fmt.Errorf("reading the step's earlier report: %w", err)
	}
	if len(got) == 0 || strings.TrimSpace(got[0].Body) == "" {
		return nil, nil
	}
	return &wroteFile{rel, got[0].Body}, nil
}
