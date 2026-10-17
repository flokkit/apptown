package workflowexec

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"time"

	"flokkit.io/forge/internal/dockerlabel"

	"flokkit.io/forge/internal/deploy/docker"

	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/gitexec"

	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/forge/agentenv"
	"flokkit.io/forge/internal/forge/integration"
	"flokkit.io/forge/internal/forge/repositoryedit"
	"flokkit.io/forge/internal/forge/tasks"
	"flokkit.io/forge/internal/forge/workflow"
)

const scriptTimeout = 10 * time.Minute
const maxLogBytes = 64 * 1024

type scriptConfig struct {
	Run            string            `json:"run"`
	Env            map[string]string `json:"env"`
	Executor       string            `json:"executor"` // scripts default local; opt into docker per step
	Image          string            `json:"image"`
	TimeoutMinutes int               `json:"timeoutMinutes"`
}

// scriptPlan is everything a script step's run and its settlement need. It is
// derived from the step alone, so a restarted forge collecting the step's
// task rebuilds the same plan.
type scriptPlan struct {
	// cache is the project's cache volume, which the script's container
	// shares with that project's other steps only.
	cache    string
	cfg      scriptConfig
	executor string
	request  execution.Request
	timeout  time.Duration
	ambient  agentenv.Ambient
	ws       string
	// candidate is set in a checked landing: the script runs on a staged copy
	// of that commit, names it in its outcome, and commits nothing (a check
	// does not change what it checks). cleanup removes the staged copy.
	candidate string
	cleanup   func()
	// folder is the run's folder, locked for as long as the step runs: a
	// remote task's result brings it forward.
	folder *gitexec.Folder
	// repository is the project's repository, the source a remote task's
	// commit is bundled from.
	repository string
	// remote is set while a remote task's result is collected.
	remote *remoteTask
}

// remoteTask names a task that ran on a worker: the commit it was given,
// which its returned commits must build on, its collected candidate, and
// what publishing that candidate answered.
type remoteTask struct {
	source    string
	candidate repositoryedit.Candidate
	published error
}

// RunScript executes a script step as a subprocess in the run's workspace.
// A Docker script runs as a recorded task (step 2 of the first-slice plan): a
// forge restart adopts it instead of running it again.
func (a *Commands) RunScript(task *workflow.Execution) {
	run := task.Run()
	// The folder is waited for before the claim, so a script that waited
	// claims only if its attempt is still current.
	ws, err := a.stepFolder(task.Context(), run)
	if err != nil {
		// Unclaimed, the step stays pending for the dispatcher to retry.
		log.Printf("run %d step %d: reading its workspace: %v; not started", run.ID, task.Step().ID, err)
		return
	}
	folder := gitexec.LockFolder(ws)
	defer folder.Release()
	claim, err := task.Claim()
	if err != nil {
		return
	}
	// The claim carries the branch authority its commit is published under.
	step := claim
	if err := a.folderClear(task.Context(), run, step, ws); err != nil {
		task.Settle(workflow.EncodeOutcome(workflow.StatusError, map[string]any{"reason": err.Error()}, nil))
		return
	}
	plan, err := a.planScript(task.Context(), step, run)
	if err != nil {
		task.Settle(workflow.EncodeOutcome(workflow.StatusError, map[string]any{"reason": err.Error()}, nil))
		return
	}
	defer plan.cleanup()
	plan.folder = folder
	if a.tasks != nil && a.tasks.Remote != nil && plan.executor != execution.Docker {
		// An isolated forge runs no project script on its own host.
		task.Settle(workflow.EncodeOutcome(workflow.StatusError, map[string]any{"reason": "this forge runs scripts only on its worker, in a container; give the step executor: docker"}, nil))
		return
	}
	if plan.executor != execution.Docker && !a.cfg.HostScripts {
		// A project's script is the project's code: it runs in a container
		// unless the operator chose no isolation for agents.
		task.Settle(workflow.EncodeOutcome(workflow.StatusError, map[string]any{"reason": "this forge runs no project script on its own host: drop executor: local so the step runs in a container (a forge started with -host-scripts allows it)"}, nil))
		return
	}
	if plan.executor == execution.Docker && a.tasks != nil {
		a.runScriptTask(task, step, run, plan)
		return
	}
	start := a.now()
	outcome := agentenv.Execute(a.runner, task.Context(), plan.executor, plan.request, start)
	if err := a.settleScript(task, step, run, plan, outcome, a.now().Sub(start)); err != nil {
		log.Printf("run %d step %d: settling the script: %v", run.ID, step.ID, err)
	}
}

func (a *Commands) planScript(ctx context.Context, step *workflow.Step, run *workflow.Run) (scriptPlan, error) {
	var cfg scriptConfig
	if err := workflow.DecodeStepConfig(step.Config, &cfg); err != nil {
		return scriptPlan{}, err
	}
	source, err := workflow.ReadSourceIn(ctx, a.db, workflow.RunRef{ID: run.ID, ProjectID: run.ProjectID})
	if err != nil {
		return scriptPlan{}, err
	}
	ws := source.Workspace(a.cfg.DataDir)
	_ = os.MkdirAll(ws, 0o755)
	candidate, pinned, err := workflow.LandingCandidateIn(ctx, a.db, run.ID)
	if err != nil {
		return scriptPlan{}, err
	}
	cleanup := func() {}
	if pinned {
		staged, remove, err := scm.StageTreeAt(ctx, source.Repository(a.cfg.DataDir), candidate.Candidate)
		if err != nil {
			return scriptPlan{}, fmt.Errorf("staging the landing's candidate %s: %w", candidate.Candidate, err)
		}
		ws, cleanup = staged, remove
	}

	// No executor named: a container, unless the operator lets project
	// scripts run on this host (-host-scripts), where it stays the host.
	executor := cfg.Executor
	if executor == "" {
		executor = execution.Docker
		if a.cfg.HostScripts {
			executor = execution.Local
		}
	}
	image := cfg.Image
	if image == "" {
		image = a.cfg.Image
	}
	var env []string
	for k, v := range cfg.Env {
		env = append(env, k+"="+v)
	}
	slices.Sort(env) // a map has no order; the request's digest needs one
	timeout := scriptTimeout
	if cfg.TimeoutMinutes > 0 {
		timeout = time.Duration(cfg.TimeoutMinutes) * time.Minute
	}
	if a.tasks != nil && a.tasks.ScriptWallClock > 0 {
		timeout = a.tasks.ScriptWallClock
	}

	// Ambient build environment (design/subsystems/registries.md): registry env +
	// generated mise/cargo config, docker steps only — the env references
	// container paths (cache volume), and local steps run on the operator's
	// own host config.
	runCmd := cfg.Run
	var amb agentenv.Ambient
	if executor == execution.Docker {
		// On this machine or the worker alike: the files travel with the
		// request, and whoever runs the container writes them beside its
		// tree, never in it.
		amb = agentenv.AmbientFor(integration.RegistryBindingsIn(ctx, a.db), ws)
		env = append(env, amb.Env...)
		if agentenv.DeclaresToolchain(ws) {
			// Implicit toolchain: install what the repo declares, expose via
			// shims, record what resolved for the receipt. Install failure
			// fails the step loudly (exit 97).
			runCmd = `export PATH="$MISE_DATA_DIR/shims:$PATH"; ` +
				`mise install -y -q || { echo "forge: mise install failed" >&2; exit 97; }; ` +
				`mise ls --current --json > /work/.forge/mise-resolved.json 2>/dev/null; ` + cfg.Run
		}
	}
	return scriptPlan{cache: agentenv.CacheVolume(ctx), cfg: cfg, executor: executor, timeout: timeout, ambient: amb, ws: ws, candidate: candidate.Candidate, cleanup: cleanup,
		repository: source.Repository(a.cfg.DataDir), request: execution.Request{
			Workspace: ws,
			Argv:      []string{"sh", "-c", runCmd},
			Env:       env,
			Files:     amb.Files,
			Timeout:   timeout,
			Image:     image,
			Name:      execution.UniqueName(fmt.Sprintf("forge-step-%d", step.ID)),
			// It mounts the folder writable: one of the folder's writers.
			Labels: []string{docker.WritesLabel + "=" + ws, fmt.Sprintf(dockerlabel.Step+"=%d", step.ID)},
		}}, nil
}

// settleScript records the step's outcome and returns the error recording it,
// if any. The step's work is committed and published first: a script that
// exited 0 passes only if its commit was published (the collecting phase
// comes before pass). Publishing is safe to repeat: settling again after a
// failure publishes nothing twice.
func (a *Commands) settleScript(task *workflow.Execution, step *workflow.Step, run *workflow.Run, plan scriptPlan, outcome execution.Outcome, took time.Duration) error {
	log := string(outcome.Output)
	if len(log) > maxLogBytes {
		log = log[:maxLogBytes] + "\n... (truncated)"
	}

	status := workflow.StatusPass
	exitCode := 0
	if outcome.TimedOut {
		status = workflow.StatusError
		exitCode = -1
		log += "\n(timed out)"
	} else if !outcome.Answered() {
		// Only a witnessed ordinary exit answers: an out-of-memory kill, a
		// run that left its container behind or could not start is the
		// forge's error, never a pass.
		status = workflow.StatusError
		exitCode = -1
		if outcome.RunErr != nil {
			log += "\n" + outcome.RunErr.Error()
		} else if outcome.CleanupErr != nil {
			log += "\n" + outcome.CleanupErr.Error()
		}
	} else if outcome.ExitCode > 0 {
		status = workflow.StatusFail
		exitCode = outcome.ExitCode
	} else if outcome.RunErr != nil {
		status = workflow.StatusError
		exitCode = -1
		log += "\n" + outcome.RunErr.Error()
	}
	evidence := map[string]any{
		"exitCode":   exitCode,
		"log":        log,
		"durationMs": took.Milliseconds(),
		"command":    plan.cfg.Run,
		"executor":   plan.executor,
	}
	if len(plan.ambient.Bound) > 0 {
		evidence["registries"] = plan.ambient.Bound
	}
	if plan.remote == nil {
		if tc := agentenv.ResolvedToolchain(plan.ws); len(tc) > 0 {
			evidence["toolchain"] = tc
		}
	} else {
		evidence["backend"], evidence["source"] = a.tasks.Profile.Name, plan.remote.source
	}
	if plan.remote != nil && plan.remote.candidate.Ref != "" {
		evidence["result"] = map[string]any{"ref": plan.remote.candidate.Ref, "commit": plan.remote.candidate.Tip, "refused": plan.remote.candidate.Refused}
	}
	if plan.candidate != "" {
		evidence["candidate"] = plan.candidate
		return task.Finish(workflow.EncodeOutcome(status, evidence, map[string]any{"exitCode": exitCode}))
	}
	var published error
	if plan.remote != nil {
		published = plan.remote.published
	} else {
		published = repositoryedit.CommitStep(task.Context(), a.db, a.cfg.DataDir, a.branches, a.publisher, run, step, a.now())
	}
	if published != nil && status == workflow.StatusPass {
		status = workflow.StatusError
		evidence["reason"] = "the step's work could not be published: " + published.Error()
	}
	return task.Finish(workflow.EncodeOutcome(status, evidence, map[string]any{"exitCode": exitCode}))
}

// keepAndPublish keeps a worker attempt's result, then publishes it. Its
// commits are collected once as its candidate in the project's repository
// (collectCandidate); judge is given the candidate before anything is
// published, and ends the attempt itself when it returns false. publish then
// publishes the candidate, or keeps it refused (nil: the result is only
// kept). Until the result exists in the project's repository, the attempt's
// workspace on the worker is its only copy, so it is held and the attempt is
// an error. A publication not decided yet (applied and not settled, or its
// outcome unknown) is asked again, which reads the branch first. One still
// undecided leaves the step and the attempt as they are, the candidate kept,
// for a restarted forge to finish: it never runs the task or publishes
// again. It returns the result as kept and what publishing answered; ok is
// false when the attempt was ended, held or left recorded here.
func (r *TaskRunner) keepAndPublish(task *workflow.Execution, attempt tasks.Attempt, hub string, judge func(repositoryedit.Candidate) bool, publish func(repositoryedit.Candidate) (repositoryedit.Candidate, error)) (candidate repositoryedit.Candidate, published error, ok bool) {
	// The task has exited: its result is collected though the step's context
	// ends (bounded by the backend's patience), so a stop is never read as a
	// result that could not be kept.
	candidate, err := r.collectCandidate(context.WithoutCancel(task.Context()), hub, task.Run(), task.Step(), attempt)
	switch {
	case errors.Is(err, execution.ErrWorkspaceGone) && attempt.Workspace != "":
		// An attempt's shared workspace the worker says is gone holds
		// nothing to keep: the work is lost. (A script's own workspace is
		// held, as any result not kept.)
		r.lostWork(task, attempt, err)
		return candidate, nil, false
	case err != nil:
		r.holdResult(task, attempt, fmt.Errorf("collecting the step's result: %w", err))
		return candidate, nil, false
	case !judge(candidate):
		return candidate, nil, false
	}
	if publish != nil {
		abandoned := false
		pending := r.settleRepeatedly(task, func() error {
			kept, err := publish(candidate)
			if errors.Is(err, repositoryedit.ErrPublicationPending) {
				if abandoned = r.abandon("publication pending"); abandoned {
					return nil
				}
				return err
			}
			candidate, published = kept, err
			return nil
		})
		switch {
		case abandoned:
			return candidate, published, false
		case pending != nil:
			log.Printf("task %s: %v; it stays recorded, and a restarted forge settles its publication from the branch", attempt.Operation, pending)
			return candidate, published, false
		case errors.Is(published, repositoryedit.ErrResultNotKept):
			r.holdResult(task, attempt, published)
			return candidate, published, false
		}
	}
	return candidate, published, !r.abandon("published")
}

// collectCandidate brings a worker task attempt's commits back once, as its
// candidate in the project's repository, hub (repositoryedit.KeepCandidate):
// the worker bundles them, and the bundle is imported through the quarantine
// and checked before its tip is kept. Nothing the worker sends is trusted
// before those checks. A candidate kept before (a retry, a restart) is found
// and not collected again. An error means the result is not kept: the
// attempt's workspace on the worker must be held. That workspace is the one
// it shares with the attempt's other invocations, or else its own.
func (r *TaskRunner) collectCandidate(ctx context.Context, hub string, run *workflow.Run, step *workflow.Step, attempt tasks.Attempt) (repositoryedit.Candidate, error) {
	n := attempt.Owner.Attempt
	if found, ok, err := repositoryedit.FindCandidate(ctx, hub, step.ID, n, attempt.Source); err != nil || ok {
		return found, err
	}
	scratch, err := os.MkdirTemp("", "forge-result-*")
	if err != nil {
		return repositoryedit.Candidate{}, err
	}
	defer os.RemoveAll(scratch)
	bundle := filepath.Join(scratch, "result.bundle")
	workspace := attempt.Operation
	if attempt.Workspace != "" {
		workspace = attempt.Workspace
	}
	var tip string
	err = r.whileUnreachable(ctx, attempt.Operation, func(ctx context.Context) (err error) {
		tip, err = r.Remote.Collect(ctx, workspace, attempt.Source, repositoryedit.StepCommitMessage(run, step), bundle)
		return err
	})
	if err != nil {
		return repositoryedit.Candidate{}, err
	}
	return repositoryedit.KeepCandidate(ctx, hub, step.ID, n, attempt.Source, tip, bundle)
}

// stepFolder is the folder run's steps work in (their lock's key).
func (a *Commands) stepFolder(ctx context.Context, run *workflow.Run) (string, error) {
	source, err := workflow.ReadSourceIn(ctx, a.db, workflow.RunRef{ID: run.ID, ProjectID: run.ProjectID})
	if err != nil {
		return "", err
	}
	return source.Workspace(a.cfg.DataDir), nil
}

// folderClear refuses a script while anything else may still write its
// folder: another script task not settled, or a container labelled as
// writing it (an agent or reviewer whose container outlived its step or the
// forge), as an agent attempt refuses them.
func (a *Commands) folderClear(ctx context.Context, run *workflow.Run, step *workflow.Step, ws string) error {
	if err := earlierTasksStopped(ctx, a.db, run, step); err != nil {
		return err
	}
	return a.writersGone(ctx, step, ws, "")
}

// writersGone is folderReady for a script: own names the task whose
// container is this attempt's own (a recovered task), which is kept.
func (a *Commands) writersGone(ctx context.Context, step *workflow.Step, ws, own string) error {
	if a.docker == nil {
		return nil
	}
	return folderReady(ctx, a.docker, step.ID, ws, own)
}
