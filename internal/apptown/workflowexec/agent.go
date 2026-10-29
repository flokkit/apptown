package workflowexec

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	piharness "github.com/flokkit/apptown/internal/apptown/agentharness/pi"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/strs"

	"github.com/flokkit/apptown/internal/apptown/integration"

	models "github.com/flokkit/apptown/internal/apptown/model"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/deploy"
)

// agent/pi step execution. AppTown supplies a scoped Work observation, builds the
// prompt and steering, then runs pi in the Docker sandbox. Model-provider keys
// stay behind the inference proxy; the sandbox receives its attributed session
// credential and explicitly bound ephemeral services.
//
// Steering: the project's compiled guidance, assigned skills (skill/* blocks),
// and resource pointers are appended to pi's system prompt from a file written
// into the workspace. Context files are always off, and what else pi may load
// is decided by the generated pi-home (pihome.go): the baked extension and the
// repo's .agents/skills — both pinned by the same snapshot the ledger pinned.
// A non-docker run keeps ambient config off entirely (HOME isn't ours there).

const agentTimeout = 15 * time.Minute
const maxAgentOutput = 24 * 1024

type agentConfig struct {
	Prompt    string               `json:"prompt"`
	Model     string               `json:"model"`    // "provider/model-pattern"; empty = workflow default, then apptownd default, then pi default
	Thinking  string               `json:"thinking"` // off|minimal|low|medium|high
	Executor  string               `json:"executor"` // docker, the only value; validation refuses any other
	Image     string               `json:"image"`    // docker image; empty = apptownd default
	Scm       integration.SCMGrant `json:"scm"`      // false | true(=read) | "read" | "write" — project-scoped GitLab token
	Skills    []string             `json:"skills"`   // guidance block names, conventionally skill/*
	Resources []string             `json:"resources"`
	// Requires names project-scoped artifact kinds that MUST exist before this
	// step runs — the same kinds a ${{latest.<kind>}} prompt reference reads.
	// Absent, the step fails loudly before spawning the agent instead of
	// building from the "(no artifact...)" placeholder (complaint #8: implement
	// dispatched with no spec-final and produced garbage).
	Requires []string `json:"requires"`
	// Services are sidecar containers on a per-step network (docker executor
	// only), reachable by name — the compose-style verify environment.
	Services []deploy.ServiceSpec `json:"services"`
	// Needs, when true, fulfills the app CONTRACT's declared needs for this step
	// (design/decisions/forge-native-contract.md, process-mode): the emulators join the
	// agent's network and their connection env (DATABASE_URL, PG*, …) is injected
	// so the agent runs the app AS PROCESSES against them — no hand-declared
	// services, exactly the deps the contract declares.
	Needs bool `json:"needs"`
	// needsRefused is why the app's declared needs were not started for this
	// step (a declaration that does not parse, or needs the catalogue refuses
	// or does not hold), "" when they were or none are declared. The agent is
	// told: it may be the change that fixes them, and refusing the step would
	// leave no governed way to (greeting-api#21).
	needsRefused string
	// Docker, when true, gives the agent a private Docker daemon.
	Docker bool `json:"docker"`
	// MayChangeNothing declares that an empty result is a complete answer
	// for this step (a maintenance lane with nothing to update): a reviewed
	// step that leaves the change empty passes with "nothing to change"
	// instead of failing "made no change", and the run then ends without
	// asking anyone to accept an empty change (workflow-spec, agent review).
	MayChangeNothing bool `json:"mayChangeNothing"`
	Output           struct {
		Kind string `json:"kind"` // attach output as artifact of this kind
		Name string `json:"name"`
		File string `json:"file"` // workspace file that IS the output (beats chat text)
	} `json:"output"`
	TimeoutMinutes int `json:"timeoutMinutes"`
	// RetryMode is set transiently by a human retry: "" (fresh session on
	// the current workspace), "continue" (resume THIS step's previous pi
	// session with the feedback as the new message), "restart" (reset the
	// worktree to the run's base first, then a fresh session). Recorded in
	// config as provenance of how the attempt ran.
	RetryMode string `json:"retryMode,omitempty"`
	// A new continuation step resumes the source step's recorded session.
	ContinueSession string `json:"continueSession,omitempty"`
	// Sendback: a human's note dispatched with apptown retry -m — the resumed
	// session's message when no gate rejection carries the words.
	Sendback string `json:"sendback,omitempty"`
}

// RunAgent executes an agent/pi step. It is a sequence of named pieces —
// preflight, slot, knobs, steering, prompt, env, sandbox, pi, harvest,
// record — so a change to one is not a change to all of them.
func (a *Executor) Run(task *workflow.Execution) {
	defer reportPanic(a.panics, "RunAgent")
	step := task.Step()
	stepID := step.ID
	run := task.Run()
	var cfg agentConfig
	cfgErr := workflow.DecodeStepConfig(step.Config, &cfg)

	attempt := a.obs.begin(task.Context(), run.ID, step.ID, "agent/pi", run.WorkflowName, run.ProjectID)
	defer attempt.settle("abandoned")
	fail := func(status workflow.StepState, evidence map[string]any) {
		attempt.settle(settlementOutcome(status, task.Finish(workflow.EncodeOutcome(status, evidence, nil))))
	}
	if cfgErr != nil {
		if _, claimErr := task.Claim(); claimErr == nil {
			fail(workflow.StatusError, map[string]any{"reason": cfgErr.Error()})
		}
		return
	}

	// Preflight: required project artifacts must exist before we spawn
	// anything. Building from a "(no artifact of kind ...)" placeholder wastes
	// a whole agent run (complaint #8: implement dispatched with no spec-final
	// and produced garbage). Checked BEFORE the concurrency slot so a missing
	// prerequisite fails instantly and holds a slot for real work.
	missing, err := workflow.MissingArtifactKindsIn(task.Context(), a.db, run.ProjectID, cfg.Requires)
	if err != nil {
		if _, claimErr := task.Claim(); claimErr == nil {
			attempt.settle(settlementOutcome(workflow.StatusError, task.Finish(workflow.EncodeOutcome(workflow.StatusError, map[string]any{"reason": fmt.Sprintf("checking required artifacts: %v", err)}, nil))))
		}
		return
	}
	if len(missing) > 0 {
		kind := missing[0]
		if _, claimErr := task.Claim(); claimErr == nil {
			attempt.settle(settlementOutcome(workflow.StatusFail, task.Finish(workflow.EncodeOutcome(workflow.StatusFail, map[string]any{
				"reason":   fmt.Sprintf("this step needs a %q artifact and the project has none yet — accept one first (product-spec produces spec-final), then re-run. Nothing was dispatched, so no work was lost.", kind),
				"requires": kind}, nil))))
		}
		return
	}

	// Slot resolution: a non-slash model value names a
	// slot; the instance/project map decides the concrete pattern. Resolved
	// here, before the wait, because the wait is the backend's: the step
	// draws a slot from the pool of the binding its model runs on
	// (model_slots.go), and queues only when that backend is full.
	k := a.resolveAgentKnobs(run, &cfg)
	if reason := strs.FirstNonBlank(executorRefusal(run, &cfg), a.dockerRefusal(k)); reason != "" {
		if _, claimErr := task.Claim(); claimErr == nil {
			fail(workflow.StatusError, map[string]any{"reason": reason})
		}
		return
	}
	// A model or capacity that cannot be read refuses the step: it never runs
	// on another model or under another limit.
	model, modelSlot, modelProfile, err := a.modelSelection.Resolve(task.Context(), run.ProjectID, k.model)
	if err != nil {
		if _, claimErr := task.Claim(); claimErr == nil {
			fail(workflow.StatusError, map[string]any{"reason": fmt.Sprintf("choosing the step's model: %v", err)})
		}
		return
	}
	binding := models.BindingOfPattern(model)
	queuedAt := a.now()
	releaseSlot, waited, err := a.capacity.Acquire(task.Context(), binding, step.ID)
	if err != nil {
		if _, claimErr := task.Claim(); claimErr == nil {
			fail(workflow.StatusError, map[string]any{"reason": err.Error()})
		}
		return
	}
	defer releaseSlot()
	if waited {
		recordEvent(task.Context(), a.db, step.RunID, "step.queued", map[string]any{"step": step.Name, "reason": "waited for a slot on " + binding, "ms": a.now().Sub(queuedAt).Milliseconds()}, a.now())
	}
	queuedMs := a.now().Sub(queuedAt).Milliseconds()
	attempt.queued(queuedAt, binding)
	if a.worker != nil {
		a.runOnWorker(task, attempt, nil, queuedMs, fail)
		return
	}

	// The folder is waited for before the claim, so a step that waited
	// claims only if its attempt is still current (the run was not stopped
	// meanwhile).
	source, err := workflow.ReadSourceIn(task.Context(), a.db, workflow.RunRef{ID: run.ID, ProjectID: run.ProjectID})
	if err != nil {
		// Unclaimed, the step stays pending for the dispatcher to retry.
		log.Printf("run %d step %d: reading its workspace: %v; not started", run.ID, step.ID, err)
		attempt.settle("unclaimed")
		return
	}
	ws, err := source.Workspace(a.cfg.DataDir)
	if err != nil {
		log.Printf("run %d step %d: selecting workspace: %v; not started", run.ID, step.ID, err)
		attempt.settle("unclaimed")
		return
	}
	folder := gitexec.LockFolder(ws)
	defer folder.Release()
	if claim, err := task.Claim(); err != nil {
		attempt.settle("unclaimed")
		return
	} else {
		*step = *claim
		attempt.claimed(claim.Attempt)
	}
	// Nothing left from before this attempt (an earlier attempt of this
	// step, a sibling whose removal failed, a script task) may still write
	// the folder this attempt resets or uses.
	if err := earlierTasksStopped(task.Context(), a.db, run, step); err != nil {
		fail(workflow.StatusError, map[string]any{"reason": err.Error()})
		return
	}
	if a.docker != nil {
		if err := a.folderReady(task.Context(), step.ID, ws); err != nil {
			fail(workflow.StatusError, map[string]any{"reason": err.Error()})
			return
		}
	}
	_ = os.MkdirAll(filepath.Join(ws, ".apptown", "resources"), 0o755)

	st, err := a.buildSteering(task.Context(), run, step, &cfg, ws, k)
	if err != nil {
		fail(workflow.StatusError, map[string]any{"reason": err.Error()})
		return
	}

	if cfg.RetryMode == "restart" {
		if err := a.restartFromBase(task.Context(), run, step, source, folder); err != nil {
			fail(workflow.StatusError, map[string]any{"reason": err.Error()})
			return
		}
	}
	// AppTown merges main in first when the branch is behind it, as before
	// a step on a worker (main_merge.go), and the agent works on it here.
	if err := a.mergeMainInFolder(task.Context(), run, step, source, folder); err != nil {
		fail(workflow.StatusError, map[string]any{"reason": err.Error()})
		return
	}
	// The step's report from its earlier attempt is put back where it was
	// declared, unless the folder still holds it (ownEarlierReport).
	earlier, err := a.ownEarlierReport(task.Context(), run, source, step, &cfg)
	if err != nil {
		fail(workflow.StatusError, map[string]any{"reason": err.Error()})
		return
	}
	if earlier != nil {
		if err := restoreReport(ws, *earlier); err != nil {
			fail(workflow.StatusError, map[string]any{"reason": "putting back the step's earlier report: " + err.Error()})
			return
		}
	}
	if cfg.Needs {
		cfg.needsRefused = a.workspaceNeedsRefusal(task.Context(), ws)
	}
	prompt := a.agentPrompt(task.Context(), run, &cfg, source, earlier)
	mode, err := workflow.ParseRetryMode(cfg.RetryMode)
	if err != nil {
		fail(workflow.StatusError, map[string]any{"reason": err.Error()})
		return
	}
	session, err := workflow.AgentSessionIn(task.Context(), a.db, workflow.RunRef{ID: run.ID, ProjectID: run.ProjectID}, step.ID, mode)
	if err != nil {
		fail(workflow.StatusError, map[string]any{"reason": fmt.Sprintf("reading agent session history: %v", err)})
		return
	}
	attempts, sessionID := session.Dispatches, session.ID
	if err := a.resumeHistory(task.Context(), run, step, sessionID, ws); err != nil {
		fail(workflow.StatusError, map[string]any{"reason": err.Error()})
		return
	}
	args := piharness.StepArgs(piharness.FolderStepPaths, sessionID, st.Text != "", model, k.thinking, prompt)

	env, scmGranted, err := a.agentEnv(task.Context(), run, step, &cfg, ws, model, k)
	if err != nil {
		fail(workflow.StatusError, map[string]any{"reason": err.Error()})
		return
	}
	if scmGranted != "" {
		// The SCM note used to be appended AFTER the steering file was
		// written and hashed, so no agent ever read it. It goes into the
		// file, the hash and the artifact now.
		st.Text += "\n# SCM access (granted to this step)\n\n" +
			"glab is preconfigured with a short-lived PROJECT-scoped GitLab\n" +
			"token: " + integration.SCMGrant(scmGranted).Label() + ".\n" +
			"This repo only — other repos are unreachable by design; work on\n" +
			"them by invoking their workflows. Never echo the environment.\n" +
			"(glab auth status misreports env-based auth; trust actual calls.)\n"
		st.Hash = deploy.ConfigHash(st.Text)
		if err := os.WriteFile(filepath.Join(ws, ".apptown", "steering.md"), []byte(st.Text), 0o644); err != nil {
			fail(workflow.StatusError, map[string]any{"reason": "writing steering: " + err.Error()})
			return
		}
		// What the agent is told is evidence: it starts only once that is
		// recorded.
		if err := workflow.ReplaceSteeringArtifactIn(context.WithoutCancel(task.Context()), a.db, step.RunID, "steering-"+step.Name, st.Text, "sha256:"+st.Hash); err != nil {
			fail(workflow.StatusError, map[string]any{"reason": "recording the steering the agent is given: " + err.Error()})
			return
		}
	}

	env, network, cleanup, err := a.startAgentSandbox(task.Context(), run, step, &cfg, ws, env, k)
	if err != nil {
		fail(workflow.StatusError, map[string]any{"reason": err.Error()})
		return
	}
	defer cleanup()

	timeout := agentTimeout
	if cfg.TimeoutMinutes > 0 {
		timeout = time.Duration(cfg.TimeoutMinutes) * time.Minute
	}

	start := a.now()
	// What the agent wrote is told by its files' modification times, which
	// the operating system stamps: they are compared on its clock.
	wroteSince := clock.System()
	attempt.run()
	a.progress.Start(stepID)
	defer workflow.CaptureProgress(task.Context(), a.db, a.progress, stepID, a.clock)
	res := a.piInvoke(task.Context(), piCall{
		Source: source, Step: step, Image: k.image, Workspace: ws,
		Args: args, Env: env, Network: network, Timeout: timeout,
		Name: execution.UniqueName(execution.StepContainer(stepID, "agent")),
		// An agent with a private Docker daemon is not re-run in place: what
		// it started there would still be running beside the re-run. A
		// retry of the step starts afresh.
		RetryEmpty: !slices.ContainsFunc(env, func(kv string) bool { return strings.HasPrefix(kv, "DOCKER_HOST=") }),
		Session:    sessionID,
	})

	h := harvestWorkspace(ws, wroteSince, res.Text, cfg.Output.File)
	attempt.harness(res)

	attempt.settle(settlementOutcome(a.recordAgentOutcome(task.Context(), agentRecord{
		task: task, cfg: &cfg, pi: res, files: h, steering: st,
		modelSlot: modelSlot, modelProfile: modelProfile, image: k.image,
		scmGranted: scmGranted, start: start, queuedMs: queuedMs, attempts: attempts,
	})))
}

// agentKnobs is the resolved model/thinking/executor/image for a step, in
// precedence order: step config, workflow defaults, catalog pin (which WINS
// for model), apptownd defaults. useDind: the private docker daemon a
// docker-executed step declares with docker: true.

// restoreReport writes an earlier report into the folder at its path, unless
// something is there already.
func restoreReport(ws string, f wroteFile) error {
	at := filepath.Join(ws, filepath.FromSlash(f.rel))
	if _, err := os.Lstat(at); err == nil || !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(at), 0o755); err != nil {
		return err
	}
	return os.WriteFile(at, []byte(f.body), 0o644)
}
