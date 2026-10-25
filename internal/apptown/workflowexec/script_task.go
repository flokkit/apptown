package workflowexec

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/diskspace"
	"github.com/flokkit/apptown/internal/durable"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/poll"

	"github.com/flokkit/apptown/internal/apptown/agentenv"
	"github.com/flokkit/apptown/internal/apptown/repositoryedit"
	"github.com/flokkit/apptown/internal/apptown/tasks"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/execution"
)

// TaskRunner runs a step's work as a recorded task through a TaskDriver
// (design/operations/workers.md, "How work travels" and "Recovery"):
//
//  1. reserve the attempt, with its admission and deadline, under an
//     operation key derived from step and attempt;
//  2. ensure the task under that key (a retry after a lost reply finds the
//     same task, and a different request under the key is refused), record
//     its identity, then start it;
//  3. wait until it exits or its deadline passes, while watching for a
//     recorded stop and for the step to stop being this attempt's;
//  4. collect: read the output, commit and publish, settle the step, and
//     only then settle the attempt and remove the task.
//
// The request an attempt is admitted with is kept beside its record, from
// its reservation until its task's release is confirmed (keepRequest):
// whatever sends it again, after a lost reply or a restart, sends those
// bytes, never a request rebuilt from settings that may have changed. A
// release the backend does not confirm is tried again (ReleaseSettled).
//
// One procedure (execute, then collect) does all of this for a new attempt
// and for one a previous AppTown process recorded; each step is safe to
// repeat. An agent step's invocations are driven by the same execute. A lost
// or failed answer from the backend is observed, never taken as absence, and
// a task is never started twice for one attempt. A restarted AppTown recovers
// a recorded task through the workflow's own dispatch (Resume, then
// RecoverScript), so its run continues when it settles.
type TaskRunner struct {
	Records *tasks.Records
	Driver  execution.TaskDriver
	// Requests is the folder admitted requests are kept in, one folder per
	// operation (<data>/tasks).
	Requests string
	// Profile is the admission every new task carries.
	Profile TaskProfile
	// Backend identifies where tasks run: which Docker daemon, or which
	// worker. Each attempt records it, and a restarted AppTown acts only on
	// attempts recorded for the backend it has now. Empty is the profile's
	// name.
	Backend string
	// Remote is set when tasks run on another machine (a apptown-worker): each
	// is given a published commit instead of a mount, its work comes back as a
	// bundle, and no script step runs on AppTown's own host.
	Remote RemoteResults
	// Now is AppTown's clock, by which a task's deadline and its
	// admission's expiry are written and judged; nil is the real one.
	Now func() time.Time
	// Poll is how often a waiting task checks that its step is still running,
	// and how long to wait before asking an unanswering backend again.
	Poll time.Duration
	// SettleTries is how many times a step's outcome is written before the
	// task is left recorded for a restarted AppTown; zero means three.
	SettleTries int
	// Patience is how long reading a finished task's result keeps asking an
	// unanswering backend; zero is taskGrace.
	Patience time.Duration
	// AbandonAt is a test seam for crash recovery: returning true for a stage
	// ("created", "started") abandons the attempt there, as an AppTown that died
	// at that point would.
	AbandonAt func(stage string) bool
	// ScriptWallClock is a test seam: when set, it is every script task's
	// wall clock in place of its step's timeout, so a test reaches a task's
	// deadline without waiting out the shortest a step can declare (a
	// minute). Zero keeps the step's own.
	ScriptWallClock time.Duration

	commands *Commands // bound by NewCommands
}

// RemoteResults returns a remote task's commits after base as a bundle
// written to dest, and the tip it reports ("" when the task changed nothing).
type RemoteResults interface {
	Collect(ctx context.Context, key, base, message, dest string) (string, error)
}

// Recoverer returns the execution of a step attempt a previous process
// admitted (workflow.Service.RecoverExecution).
type Recoverer interface {
	RecoverExecution(context.Context, ids.ID, int64) (*workflow.Execution, error)
}

// Resume sorts the unsettled tasks a previous AppTown process recorded, at
// startup. A task whose step still belongs to its attempt is returned, for
// the workflow's dispatch to recover in its stage's order (RecoverScript).
// A task whose step moved on is stopped and settled now. A task recorded
// as refused (nothing was created) has its step failed, if still its, and
// is settled, whatever the backend. A task recorded for another backend
// than this AppTown's is left as it is, and reported.
func (r *TaskRunner) Resume(ctx context.Context, work Recoverer) ([]ids.ID, error) {
	if r == nil || r.commands == nil {
		return nil, nil
	}
	attempts, err := r.Records.Unsettled(ctx)
	if err != nil {
		return nil, err
	}
	var adopt []ids.ID
	for _, attempt := range attempts {
		if attempt.Observed == tasks.Refused {
			// Nothing was created: finish from the record, on any backend.
			task, err := work.RecoverExecution(ctx, attempt.Owner.StepID, attempt.Owner.Attempt)
			switch {
			case errors.Is(err, workflow.ErrStepAttemptLost):
				r.settleRefused(ctx, nil, attempt)
			case err != nil:
				log.Printf("task %s: recovering its refused step: %v; it stays recorded", attempt.Operation, err)
			default:
				r.settleRefused(ctx, task, attempt)
			}
			continue
		}
		if attempt.Backend != r.backend() {
			log.Printf("task %s was admitted to backend %q, and this AppTown runs tasks on %q; it is left recorded and its step running", attempt.Operation, attempt.Backend, r.backend())
			continue
		}
		if attempt.Observed == tasks.Held {
			// Its workspace holds work AppTown did not keep: it is not
			// released. A step still the attempt's (a crash between the
			// hold and the step's error) is finished as an error.
			log.Printf("task %s: %s; its workspace on the worker is held", attempt.Operation, notKept(attempt.Detail))
			task, err := work.RecoverExecution(ctx, attempt.Owner.StepID, attempt.Owner.Attempt)
			switch {
			case err == nil:
				r.failStep(task, attempt.Operation, "running the step's task: "+notKept(attempt.Detail)) // it stays held either way
			case !errors.Is(err, workflow.ErrStepAttemptLost):
				log.Printf("task %s: recovering its held step: %v; it stays recorded", attempt.Operation, err)
			}
			continue
		}
		_, err := work.RecoverExecution(ctx, attempt.Owner.StepID, attempt.Owner.Attempt)
		switch {
		case errors.Is(err, workflow.ErrStepAttemptLost) && r.Remote != nil && attempt.Source != "" && attempt.Observed == tasks.Collecting && attempt.Invocation != tasks.ReleaseBuildInvocation:
			// Its task exited and its result was being collected when its
			// step ended. The step ending is not permission to discard the
			// result: the workspace is released only once the result is
			// kept in the project's repository, and held otherwise. A
			// release build's result is its images, which a later attempt
			// builds again: its task is stopped and released (below).
			kept, err := r.commands.resultKept(ctx, attempt)
			switch {
			case err != nil:
				log.Printf("task %s: finding its kept result: %v; it stays recorded", attempt.Operation, err)
			case kept:
				r.stopTask(ctx, attempt, "its step no longer belongs to this attempt")
			default:
				why := "its step ended before its result was kept"
				switch err := r.Records.Hold(ctx, attempt.Operation, why); {
				case refusedByRecord(err):
					log.Printf("task %s: its hold was not recorded: %v; its record stands", attempt.Operation, err)
				case err != nil:
					log.Printf("task %s: recording its hold: %v; it stays recorded", attempt.Operation, err)
				default:
					log.Printf("task %s: %s; its workspace on the worker is held", attempt.Operation, notKept(why))
				}
			}
		case errors.Is(err, workflow.ErrStepAttemptLost):
			r.stopTask(ctx, attempt, "its step no longer belongs to this attempt")
		case err != nil:
			log.Printf("task %s: recovering its step: %v; it stays recorded", attempt.Operation, err)
		default:
			adopt = append(adopt, attempt.Owner.StepID)
		}
	}
	return adopt, nil
}

// TaskProfile is an administrator's execution profile: its name and the
// limits and network policy its tasks run under. Zero limits impose none.
type TaskProfile struct {
	Name    string
	Limits  execution.Limits
	Network execution.NetworkPolicy
}

const maxTaskOutput = 16 << 20

// taskGrace is how long past its wall-clock deadline a task's admission
// stays valid, for AppTown to observe and stop it; and how long a result
// is asked for when the backend does not answer.
const taskGrace = 5 * time.Minute

var errAbandoned = errors.New("abandoned by the test seam")

func (r *TaskRunner) abandon(stage string) bool { return r.AbandonAt != nil && r.AbandonAt(stage) }

// now reads the task runner's clock.
func (r *TaskRunner) now() time.Time { return clock.Or(r.Now)() }

func (r *TaskRunner) poll() time.Duration {
	if r.Poll > 0 {
		return r.Poll
	}
	return 2 * time.Second
}

func (r *TaskRunner) backend() string {
	if r.Backend != "" {
		return r.Backend
	}
	return r.Profile.Name
}

// unanswered is an error that says nothing about the task: the backend
// could not be asked, or did not answer in time.
func unanswered(err error) bool {
	return errors.Is(err, execution.ErrWorkerUnavailable) || errors.Is(err, execution.ErrTaskUnobservable) ||
		errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}

// untilAnswered repeats call while the backend does not answer, until the
// time given. Every call is bounded by that time: a backend that never
// replies cannot hold the caller past it. Any other result, or silence past
// it, is returned.
func (r *TaskRunner) untilAnswered(ctx context.Context, operation string, until time.Time, call func(context.Context) error) error {
	for {
		callCtx, cancel := r.waitUntil(ctx, until)
		err := call(callCtx)
		cancel()
		if !unanswered(err) || !r.now().Before(until) || ctx.Err() != nil {
			return err
		}
		log.Printf("task %s: %v; asking again", operation, err)
		select {
		case <-time.After(r.poll()):
		case <-ctx.Done():
			return err
		}
	}
}

// waitUntil bounds a call by a time on the runner's clock. A context's
// deadline is read on the real clock, so the call is given the time left
// until then, not the time itself: an AppTown clock ahead or behind the real
// one neither lengthens nor cuts a real wait.
func (r *TaskRunner) waitUntil(ctx context.Context, until time.Time) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, until.Sub(r.now()))
}

// whileUnreachable is untilAnswered for a result's collection: it waits up
// to Patience (taskGrace) for an unanswering backend.
func (r *TaskRunner) whileUnreachable(ctx context.Context, operation string, call func(context.Context) error) error {
	patience := r.Patience
	if patience <= 0 {
		patience = taskGrace
	}
	return r.untilAnswered(ctx, operation, r.now().Add(patience), call)
}

// briefly bounds a clean-up call (stop, remove) that must not hang. A
// clean-up runs though its caller stops.
func briefly(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
}

func taskOperation(step *workflow.Step) string {
	return fmt.Sprintf("step-%d-attempt-%d", step.ID, step.OwnedAttempt())
}

// runOnce guards a shell command so that a second start of its container,
// however it happens, does not run it again: the second start exits with the
// first run's code, or 125 if the first run never finished.
func runOnce(argv []string) []string {
	if len(argv) != 3 || argv[0] != "sh" || argv[1] != "-c" {
		return argv
	}
	const marker = "/tmp/.forge-task-exit"
	return []string{"sh", "-c", `if [ -s ` + marker + ` ]; then exit "$(cat ` + marker + `)"; elif [ -e ` + marker + ` ]; then exit 125; fi; ` +
		`: > ` + marker + `; ( ` + argv[2] + "\n" + ` ); code=$?; echo "$code" > ` + marker + `; exit "$code"`}
}

// admittedRequest is what a task was admitted with, kept in
// <Requests>/<operation>/request.json (0600) from its reservation until its
// release is confirmed. Recovery sends exactly this request.
type admittedRequest struct {
	Task execution.TaskSpec `json:"task"`
	// Registries are the registry bindings a script's request was given,
	// for its evidence: a recovered step reports what it was admitted with.
	Registries map[string]string `json:"registries,omitempty"`
	// Agent is what an agent step's invocation was admitted with, for its
	// settlement (agent_attempt.go).
	Agent *agentAdmission `json:"agent,omitempty"`
}

const requestFile = "request.json"

// requestDir is the folder an operation's admitted request is kept in.
func (r *TaskRunner) requestDir(operation string) (string, error) {
	if r.Requests == "" {
		return "", errors.New("this AppTown has no folder for admitted task requests")
	}
	if operation == "" || operation == "." || operation == ".." || strings.ContainsAny(operation, `/\`) {
		return "", fmt.Errorf("an operation key cannot name a folder: %q", operation)
	}
	return filepath.Join(r.Requests, operation), nil
}

// keepRequest keeps the request an attempt is admitted with, unless one is
// kept already, and returns the kept one: the first request admitted under
// an operation is the one it runs. The file is written whole and linked into
// place, which never replaces one there (durable.Write).
func (r *TaskRunner) keepRequest(operation string, in admittedRequest) (admittedRequest, error) {
	if kept, found, err := r.storedRequest(operation); err != nil || found {
		return kept, err
	}
	dir, err := r.requestDir(operation)
	if err != nil {
		return admittedRequest{}, err
	}
	body, err := json.Marshal(in)
	if err != nil {
		return admittedRequest{}, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return admittedRequest{}, err
	}
	// Its inputs' blobs are kept beside it, first: a recovered request can
	// send any the backend no longer holds.
	if err := keepBlobs(dir, in.Task.Inputs); err != nil {
		return admittedRequest{}, err
	}
	existed, err := durable.Write(dir, requestFile, body, 0o600, true)
	if existed {
		kept, _, err := r.storedRequest(operation)
		return kept, err
	}
	return in, err
}

// storedRequest reads the request kept for an operation; found is false when
// none is kept.
func (r *TaskRunner) storedRequest(operation string) (req admittedRequest, found bool, err error) {
	dir, err := r.requestDir(operation)
	if err != nil {
		return req, false, err
	}
	body, err := os.ReadFile(filepath.Join(dir, requestFile))
	if errors.Is(err, fs.ErrNotExist) {
		return req, false, nil
	}
	if err != nil {
		return req, false, err
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return req, false, fmt.Errorf("the task's kept request is unreadable: %w", err)
	}
	for i, in := range req.Task.Inputs {
		blob, err := os.ReadFile(filepath.Join(dir, blobsDir, in.Blob+".tar"))
		if err != nil {
			return req, false, fmt.Errorf("the task's kept input %s: %w", in.Path, err)
		}
		if req.Task.Inputs[i], err = in.WithBlob(blob); err != nil {
			return req, false, err
		}
	}
	return req, true, nil
}

// blobsDir holds a kept request's input blobs, by digest.
const blobsDir = "blobs"

// keepBlobs writes each input's blob beside a request, whole
// (durable.Write), unless it is there already (a blob is named by its
// content).
func keepBlobs(dir string, inputs []execution.Input) error {
	if len(inputs) == 0 {
		return nil
	}
	blobs := filepath.Join(dir, blobsDir)
	if err := os.MkdirAll(blobs, 0o700); err != nil {
		return err
	}
	for _, in := range inputs {
		if _, err := os.Stat(filepath.Join(blobs, in.Blob+".tar")); err == nil {
			continue
		}
		blob := in.BlobTar()
		if blob == nil {
			return fmt.Errorf("the input %s carries no blob to keep", in.Path)
		}
		if _, err := durable.Write(blobs, in.Blob+".tar", blob, 0o600, false); err != nil {
			return err
		}
	}
	// A blob already there may have been left by a write whose directory
	// sync failed: the directory is synced before the request relies on it.
	return durable.SyncDir(blobs)
}

// dropRequest removes an operation's kept request, once nothing of its task
// is held on the backend.
func (r *TaskRunner) dropRequest(operation string) {
	dir, err := r.requestDir(operation)
	if err == nil {
		err = os.RemoveAll(dir)
	}
	if err != nil {
		log.Printf("task %s: removing its kept request: %v", operation, err)
	}
}

// release removes a settled attempt's task from the backend and records the
// backend's confirmation, then removes its kept request. It reports whether
// the release is confirmed; one that is not is tried again by
// ReleaseSettled.
func (r *TaskRunner) release(ctx context.Context, operation string, ref execution.TaskRef) bool {
	ctx, cancel := briefly(ctx)
	defer cancel()
	if err := r.Driver.Release(ctx, ref); err != nil {
		log.Printf("task %s: removing it: %v; it is tried again later", operation, err)
		return false
	}
	if err := r.Records.ConfirmRelease(ctx, operation); err != nil {
		log.Printf("task %s: recording its release: %v; it is tried again later", operation, err)
		return false
	}
	r.dropRequest(operation)
	return true
}

// releaseBatch bounds how many releases one pass of ReleaseSettled tries.
const releaseBatch = 100

// ReleaseSettled tries again, once each, to release the settled tasks on
// this AppTown's backend whose release the backend has not confirmed, and
// records each it confirms. Release is idempotent: a task already gone is
// released. It returns how many releases were confirmed.
//
// An unreleased row does not prove that what its key names is its task.
// Rows settled before releases were recorded (migration 97) all arrive here,
// including attempts refused because another request's task held their key;
// such a row may have no provider ID. So each release first re-establishes
// ownership (releaseOwn); the migration does not guess from old details
// which rows held nothing.
func (r *TaskRunner) ReleaseSettled(ctx context.Context) (int, error) {
	if r == nil {
		return 0, nil
	}
	pending, err := r.Records.Unreleased(ctx, r.backend(), releaseBatch)
	if err != nil {
		return 0, err
	}
	confirmed := 0
	for _, attempt := range pending {
		if ctx.Err() != nil {
			break
		}
		if r.releaseOwn(ctx, attempt) {
			confirmed++
		}
	}
	// A shared workspace goes once every task in it is released.
	workspaces, err := r.Records.UnreleasedWorkspaces(ctx, r.backend(), releaseBatch)
	if err != nil {
		return confirmed, err
	}
	for _, workspace := range workspaces {
		if ctx.Err() != nil {
			break
		}
		if r.releaseWorkspace(ctx, workspace) {
			confirmed++
		}
	}
	return confirmed, nil
}

// releaseWorkspace removes a shared workspace from the backend once every
// task recorded in it is settled and released, and records the backend's
// confirmation. A workspace with a task that is not (one whose outcome is
// unknown holds it) is left as it is. It reports whether the release is
// confirmed; one that is not is tried again by ReleaseSettled.
func (r *TaskRunner) releaseWorkspace(ctx context.Context, workspace string) bool {
	if workspace == "" {
		return true
	}
	ctx, cancel := briefly(ctx)
	defer cancel()
	ready, err := r.Records.UnreleasedWorkspaces(ctx, r.backend(), releaseBatch)
	if err != nil {
		log.Printf("workspace %s: reading whether it can be released: %v; it is tried again later", workspace, err)
		return false
	}
	if !slices.Contains(ready, workspace) {
		return false // held by a task not released, or released already
	}
	if err := r.Driver.ReleaseWorkspace(ctx, workspace); err != nil {
		log.Printf("workspace %s: removing it: %v; it is tried again later", workspace, err)
		return false
	}
	if err := r.Records.ConfirmWorkspaceRelease(ctx, r.backend(), workspace); err != nil {
		log.Printf("workspace %s: recording its release: %v; it is tried again later", workspace, err)
		return false
	}
	return true
}

// stopWithin stops a task whose outcome is being given up, asking an
// unanswering backend again for Patience. It reports whether the stop was
// confirmed.
func (r *TaskRunner) stopWithin(ctx context.Context, attempt tasks.Attempt, ref execution.TaskRef) bool {
	ctx = context.WithoutCancel(ctx) // a stop runs though its caller stops
	err := r.whileUnreachable(ctx, attempt.Operation, func(ctx context.Context) error { return r.Driver.Stop(ctx, ref) })
	if err != nil {
		log.Printf("task %s: stopping it: %v", attempt.Operation, err)
	}
	return err == nil
}

// releaseOwn releases a settled attempt's task only after observing that
// what its key holds on the backend is that task: the same provider ID or the
// same request digest. A different request's task is left alone and the
// attempt is recorded released, since nothing of its own is held under the
// key. Nothing under the key is released as usual (clearing anything left
// with it). A backend that cannot be asked leaves the attempt for the next
// pass: unknown ownership is not permission to remove.
func (r *TaskRunner) releaseOwn(ctx context.Context, attempt tasks.Attempt) bool {
	ctx, cancel := briefly(ctx)
	obs, err := r.Driver.Observe(ctx, execution.TaskRef{Key: attempt.Operation})
	cancel()
	switch {
	case err != nil:
		log.Printf("task %s: finding it to remove it: %v; it is tried again later", attempt.Operation, err)
		return false
	case obs.State != execution.TaskAbsent && !owns(attempt, obs):
		log.Printf("task %s: a different request's task holds its key; it was left alone and nothing of this attempt is held", attempt.Operation)
		ctx, cancel := briefly(ctx)
		defer cancel()
		if err := r.Records.ConfirmRelease(ctx, attempt.Operation); err != nil {
			log.Printf("task %s: recording that nothing of it is held: %v; it is tried again later", attempt.Operation, err)
			return false
		}
		r.dropRequest(attempt.Operation)
		return true
	}
	return r.release(ctx, attempt.Operation, execution.TaskRef{Key: attempt.Operation, ProviderID: attempt.ProviderID})
}

// owns reports whether an observed task is the attempt's own: the provider
// ID the attempt recorded, or the request digest it was made for.
func owns(attempt tasks.Attempt, obs execution.TaskObservation) bool {
	return (attempt.ProviderID != "" && obs.ID == attempt.ProviderID) || (obs.Request != "" && obs.Request == attempt.Digest)
}

// taskSpec is the request for a script's task under the admission recorded
// for it. A remote task is given source, a published commit, and nothing
// from AppTown's host.
func (a *Commands) taskSpec(attempt tasks.Attempt, plan scriptPlan) execution.TaskSpec {
	req := agentenv.Decorate(plan.request, a.now(), plan.cache)
	spec := execution.TaskSpec{Key: attempt.Operation, Image: req.Image, Argv: runOnce(req.Argv), Env: req.Env, Workspace: req.Workspace, WorkspaceRO: req.WorkspaceRO,
		Mounts: req.Mounts, ExtraHosts: req.ExtraHosts, Labels: req.Labels, Files: req.Files, Admission: attempt.Admitted, Request: attempt.Digest}
	if a.tasks.Remote != nil {
		// The worker makes its own checkout: nothing here names AppTown's
		// folder, including the label marking a writer of it.
		spec.Workspace, spec.Mounts, spec.ExtraHosts = "", nil, nil
		spec.Labels = slices.DeleteFunc(slices.Clone(spec.Labels), func(l string) bool { return strings.HasPrefix(l, docker.WritesLabel+"=") })
		spec.Source = &execution.TaskSource{Repository: plan.repository, Commit: attempt.Source}
	}
	return spec
}

// requestDigest names what the task runs and under which admission, except
// when it expires. A checked landing's script runs on a freshly staged copy
// of its candidate, so the candidate, not that copy's path, identifies what
// it runs on; a remote task is identified by its source commit.
func (a *Commands) requestDigest(plan scriptPlan, commit string, admitted execution.Admission) string {
	// The labels, and so the time given here, are left out of the digest.
	req := agentenv.Decorate(plan.request, time.Time{}, plan.cache)
	source := req.Workspace
	switch {
	case plan.candidate != "":
		source = "candidate:" + plan.candidate
	case commit != "":
		source = "commit:" + commit
	}
	admitted.Expires = time.Time{}
	// Labels are left out: they name when and by whom the task was made.
	// The generated files are part of what it runs: their paths, modes and
	// bytes, so a task is never adopted under other configuration.
	canonical, _ := json.Marshal(struct {
		Image, Workspace string
		Argv, Env        []string
		Mounts, Hosts    []string
		Files            []execution.FileIdentity
		ReadOnly         bool
		Admission        execution.Admission
	}{req.Image, source, runOnce(req.Argv), req.Env, req.Mounts, req.ExtraHosts, execution.IdentifyFiles(req.Files), req.WorkspaceRO, admitted})
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

// runScriptTask reserves a new attempt's task and drives it.
func (a *Commands) runScriptTask(task *workflow.Execution, step *workflow.Step, run *workflow.Run, plan scriptPlan) {
	// The task lives by its durable record, not a context: what a stopped
	// AppTown leaves running is collected on restart.
	ctx := context.WithoutCancel(task.Context())
	source := ""
	if a.tasks.Remote != nil {
		source = plan.candidate
		if source == "" {
			// A remote task works from the change as committed and published.
			var err error
			if source, err = repositoryedit.PublishTaskSource(ctx, a.db, a.cfg.DataDir, a.branches, run, step); err != nil {
				a.finish(task, workflow.StatusError, err.Error())
				return
			}
		}
	}
	now := a.tasks.now()
	limits := a.tasks.Profile.Limits
	limits.WallClock = plan.timeout
	admitted := execution.Admission{Profile: a.tasks.Profile.Name, Limits: limits, Network: a.tasks.Profile.Network, Expires: now.Add(plan.timeout + taskGrace)}
	attempt, err := a.tasks.Records.Reserve(ctx, tasks.Admission{Operation: taskOperation(step), Invocation: tasks.ScriptInvocation,
		Owner:   tasks.Owner{ProjectID: run.ProjectID, RunID: run.ID, StepID: step.ID, Attempt: step.OwnedAttempt()},
		Backend: a.tasks.backend(), Container: taskOperation(step), Digest: a.requestDigest(plan, source, admitted), Source: source,
		Admitted: admitted, Deadline: now.Add(plan.timeout)})
	if err != nil {
		a.finish(task, workflow.StatusError, "recording the task: "+err.Error())
		return
	}
	// The request is kept before anything is asked of the backend. A
	// reservation found again (a retry) finds its request kept too.
	kept, err := a.tasks.keepRequest(attempt.Operation, admittedRequest{Task: a.taskSpec(attempt, plan), Registries: plan.ambient.Bound})
	if err == nil && kept.Task.Request != attempt.Digest {
		err = errors.New("the kept request is not the one admitted")
	}
	if err != nil {
		a.tasks.stopAndFail(task, attempt, "keeping the task's request: "+err.Error())
		return
	}
	// Checked again now that this task is recorded: another admitted at the
	// same moment sees this one, and this one sees it.
	if err := earlierTasksStopped(ctx, a.db, run, step); err != nil {
		a.tasks.stopAndFail(task, attempt, err.Error())
		return
	}
	if attempt, code, timedOut, _, ok := a.tasks.execute(task, a.db, attempt, kept.Task, taskKind{}); ok {
		a.collect(task, plan, attempt, code, timedOut)
	}
}

// RecoverScript continues, in the workflow's dispatch, the task a previous
// AppTown process recorded for this step's attempt.
func (a *Commands) RecoverScript(task *workflow.Execution) {
	step, run := task.Step(), task.Run()
	attempt, err := a.tasks.Records.Get(task.Context(), taskOperation(step))
	if err != nil {
		log.Printf("run %d step %d: reading its recorded task: %v; it stays running", run.ID, step.ID, err)
		return
	}
	if attempt.Admitted.Profile == "" || attempt.Deadline.IsZero() {
		a.tasks.failTask(task, attempt, fmt.Errorf("the task was recorded without its admission; its outcome is unknown"))
		return
	}
	ws, err := a.stepFolder(task.Context(), run)
	if err != nil {
		log.Printf("run %d step %d: finding its folder: %v; its task stays recorded", run.ID, step.ID, err)
		return
	}
	folder := gitexec.LockFolder(ws)
	defer folder.Release()
	// It may have waited for the folder: the attempt must still be current.
	// Only a confirmed loss stops its task; a check that could not be made
	// leaves the task recorded, to be recovered later.
	if err := task.Current(); errors.Is(err, workflow.ErrStepAttemptLost) {
		log.Printf("run %d step %d: its attempt is no longer current; its task is stopped", run.ID, step.ID)
		a.tasks.stopTask(task.Context(), attempt, "its step's attempt ended while it waited for the folder")
		return
	} else if err != nil {
		log.Printf("run %d step %d: checking its attempt: %v; its task stays recorded", run.ID, step.ID, err)
		return
	}
	// Whatever recovery does next touches the folder (starting the task,
	// or collecting and publishing its result), so nothing else may still
	// be writing it. A task already started is left running and recovered
	// later; one never started is stopped and its step fails (below).
	if attempt.Observed != tasks.Reserved || attempt.StopWanted {
		if err := a.writersGone(task.Context(), step, ws, attempt.Operation); err != nil {
			log.Printf("run %d step %d: %v; its task stays recorded", run.ID, step.ID, err)
			return
		}
	}
	// A task recorded but never started is a new writer when recovery starts
	// it: it waits, like any attempt, while another script task of the run
	// has not confirmed that it stopped. Refused, it is stopped and removed
	// (it never ran) and its step fails; a task already started is only
	// observed and settled.
	if attempt.Observed == tasks.Reserved && !attempt.StopWanted {
		err := earlierTasksStopped(task.Context(), a.db, run, step)
		if err == nil {
			err = a.writersGone(task.Context(), step, ws, attempt.Operation)
		}
		if err != nil {
			a.tasks.stopAndFail(task, attempt, err.Error())
			return
		}
	}
	kept, found, err := a.tasks.storedRequest(attempt.Operation)
	if err != nil {
		a.tasks.stopAndFail(task, attempt, "reading the task's kept request: "+err.Error())
		return
	}
	// The plan is what the step's outcome is settled with; the request sent
	// is the one kept at admission, whatever the settings are now.
	plan, err := a.planScript(task.Context(), step, run)
	if err != nil {
		a.tasks.failTask(task, attempt, err)
		return
	}
	defer plan.cleanup()
	plan.folder = folder
	spec := kept.Task
	switch {
	case found && spec.Request != attempt.Digest:
		a.tasks.stopAndFail(task, attempt, "the task's kept request is not the one admitted")
		return
	case found:
		plan.ambient.Bound = kept.Registries
		if attempt.Observed == tasks.Reserved && spec.Workspace != "" {
			// A local task mounts a folder named in its request (a landing's
			// staged copy); one that is gone cannot be sent again.
			if _, err := os.Stat(spec.Workspace); err != nil {
				a.tasks.stopAndFail(task, attempt, "the task's folder is gone: "+err.Error())
				return
			}
		}
	case a.requestDigest(plan, attempt.Source, attempt.Admitted) != attempt.Digest:
		// Recorded before requests were kept: the request is rebuilt, and
		// refused if the step's definition or this AppTown's configuration
		// changed since, so what would run now is not what was admitted.
		a.tasks.stopAndFail(task, attempt, "the step's request changed since its task was recorded")
		return
	default:
		spec = a.taskSpec(attempt, plan)
	}
	if attempt, code, timedOut, _, ok := a.tasks.execute(task, a.db, attempt, spec, taskKind{}); ok {
		a.collect(task, plan, attempt, code, timedOut)
	}
}

// taskKind is what differs when the task driven is an agent or review
// step's invocation rather than a script's task.
type taskKind struct {
	// invocation: the step's reasons name an invocation; a task the worker
	// says is gone is lost work, watched for while waiting; a failed wait
	// whose stop the worker does not confirm holds the workspace; and the
	// attempt's settled invocations and its workspace are released once the
	// attempt is ended here (releaseAttempt).
	invocation bool
	// follow, if set, streams the task's output while it runs, and says how
	// much of it the live feed carries.
	follow func(context.Context, execution.TaskRef) <-chan int64
}

// execute takes an attempt from wherever it was recorded to a decided
// outcome: ensured, started, waited for (while watching for a recorded stop
// and for the step to stop being this attempt's), and its exit or timeout
// recorded, with the stop for a timeout, before anything else can fail, so a
// restart collects it as decided. spec is the request it was admitted with.
// It returns the attempt with its provider identity, its exit code, whether
// it timed out, and how much output the live feed carries; ok is false when
// the attempt was ended (or abandoned) here.
func (r *TaskRunner) execute(task *workflow.Execution, db database.Queryer, attempt tasks.Attempt, spec execution.TaskSpec, kind taskKind) (_ tasks.Attempt, code int, timedOut bool, summarized int64, ok bool) {
	// The task lives by its durable record, not a context: what a stopped
	// AppTown leaves running is collected on restart.
	ctx := context.WithoutCancel(task.Context())
	operation := attempt.Operation
	noun := "task"
	if kind.invocation {
		noun = "invocation"
	}
	ended := func() {
		if kind.invocation {
			r.releaseAttempt(ctx, attempt)
		}
	}
	fail := func(reason string) {
		r.stopAndFail(task, attempt, reason)
		ended()
	}
	stopped, expired := "the step's "+noun+" was stopped", "the "+noun+"'s admission expired before it started"
	decidedTimeout := attempt.TimedOut
	switch {
	case attempt.Observed == tasks.Collecting && attempt.ExitCode != nil && attempt.ProviderID != "" && (decidedTimeout || !attempt.StopWanted):
		// The outcome was decided before a failed settlement or a restart (an
		// exit, or a timeout with its stop): it is collected as decided,
		// never observed again. A decided timeout carries its own stop; a
		// decided exit is not a settled step: a stop recorded since is
		// carried out instead.
		return attempt, *attempt.ExitCode, decidedTimeout, 0, true
	case attempt.StopWanted:
		fail(stopped)
		return attempt, 0, false, 0, false
	}
	id, err := r.provide(ctx, attempt, spec)
	switch {
	case errors.Is(err, errAbandoned):
		return attempt, 0, false, 0, false
	case errors.Is(err, execution.ErrTaskConflict), errors.Is(err, execution.ErrUnsupported), errors.Is(err, diskspace.ErrRefused):
		// Another request's task holds the key, or the backend refused the
		// request before creating anything (a request it cannot run, a
		// Docker selection it could not pin, too little disk free). Nothing of this attempt's
		// exists and nothing is touched: the step fails, and the attempt,
		// which made nothing, is settled rather than left unknown.
		r.refuse(task, attempt, err)
		ended()
		return attempt, 0, false, 0, false
	case errors.Is(err, execution.ErrExpired), errors.Is(err, errPastDeadline):
		// Not created now; one created before a crash never ran: remove it
		// (if it is this attempt's), and fail the step.
		fail(expired)
		return attempt, 0, false, 0, false
	case err != nil:
		r.failTask(task, attempt, err)
		return attempt, 0, false, 0, false
	}
	attempt.ProviderID = id
	ref := execution.TaskRef{Key: operation, ProviderID: id}
	obs, err := r.begin(ctx, attempt)
	switch {
	case errors.Is(err, errAbandoned):
		return attempt, 0, false, 0, false
	case refusedByRecord(err):
		r.endAsRecorded(task, attempt, err)
		return attempt, 0, false, 0, false
	case kind.invocation && errors.Is(err, errTaskGone):
		r.lostWork(task, attempt, err)
		return attempt, 0, false, 0, false
	case errors.Is(err, execution.ErrExpired), errors.Is(err, errPastDeadline):
		fail(expired)
		return attempt, 0, false, 0, false
	case err != nil:
		r.stopQuietly(ctx, attempt, ref)
		r.failTask(task, attempt, err)
		return attempt, 0, false, 0, false
	}
	code, why := obs.ExitCode, "exited"
	if obs.State != execution.TaskExited {
		// Only a task still running is held to what remains of its budget;
		// one already seen to have exited keeps its result.
		follow, followed := context.WithCancel(ctx)
		var seen <-chan int64
		if kind.follow != nil {
			seen = kind.follow(follow, ref)
		}
		code, why, err = r.awaitTask(ctx, attempt, ref, func(ctx context.Context) string {
			if why := stepOwnership(ctx, task, r.Records, operation); why != "" || !kind.invocation {
				return why
			}
			// A worker that says the task is gone has lost it: that is not
			// waited out until the deadline. Not answering is not gone.
			if r.gone(ctx, ref) {
				return "gone"
			}
			return ""
		})
		followed()
		if seen != nil {
			summarized = <-seen
		}
		switch {
		case why == "lost":
			r.stopTask(follow, attempt, "its step no longer belongs to this attempt")
			ended()
			return attempt, 0, false, 0, false
		case why == "stop":
			fail(stopped)
			return attempt, 0, false, 0, false
		case why == "gone":
			r.lostWork(task, attempt, errTaskGone)
			return attempt, 0, false, 0, false
		case err != nil && kind.invocation && r.gone(ctx, ref):
			// A task the worker says is gone is lost work, not an exit.
			r.lostWork(task, attempt, err)
			return attempt, 0, false, 0, false
		case err != nil && kind.invocation && !r.stopWithin(follow, attempt, ref):
			r.holdResult(task, attempt, fmt.Errorf("waiting for the invocation: %w; the worker did not confirm its stop, so its outcome is unknown", err))
			return attempt, 0, false, 0, false
		case err != nil:
			if !kind.invocation {
				r.stopQuietly(follow, attempt, ref)
			}
			r.failTask(task, attempt, err)
			return attempt, 0, false, 0, false
		}
	}
	timedOut = why == "timed out"
	if timedOut {
		code = -1
		err = r.Records.TimedOut(ctx, operation) // the timeout and its stop are one record
	} else {
		err = r.Records.Exited(ctx, operation, code)
	}
	switch {
	case refusedByRecord(err):
		// The record decided otherwise meanwhile: nothing is collected.
		r.endAsRecorded(task, attempt, err)
		return attempt, 0, false, 0, false
	case err != nil:
		// Not recorded, but observed: it is collected, and a restarted
		// AppTown observes it again.
		log.Printf("task %s: recording its outcome: %v", operation, err)
	}
	if r.abandon("exited") {
		return attempt, 0, false, 0, false
	}
	return attempt, code, timedOut, summarized, true
}

// lostWork fails the step of an attempt whose work the worker lost.
func (r *TaskRunner) lostWork(task *workflow.Execution, attempt tasks.Attempt, cause error) {
	r.failTask(task, attempt, fmt.Errorf("the worker lost this step's work (%w)", cause))
}

// gone reports whether the backend answers that the task does not exist.
// Not answering is not gone.
func (r *TaskRunner) gone(ctx context.Context, ref execution.TaskRef) bool {
	observed, err := r.Driver.Observe(ctx, ref)
	return err == nil && observed.State == execution.TaskAbsent
}

// collect takes a decided outcome to settled: a timed-out task is stopped,
// the output read, the step settled, then the attempt settled and the task
// removed. If the step cannot be settled, everything stays recorded for a
// restarted AppTown, which collects the same decided outcome.
func (a *Commands) collect(task *workflow.Execution, plan scriptPlan, attempt tasks.Attempt, code int, timedOut bool) {
	// The task lives by its durable record, not a context: what a stopped
	// AppTown leaves running is collected on restart.
	ctx := context.WithoutCancel(task.Context())
	operation := attempt.Operation
	ref := execution.TaskRef{Key: operation, ProviderID: attempt.ProviderID}
	outcome := execution.Outcome{Termination: execution.Exited, ExitCode: code}
	if !timedOut {
		// How it ended, not only its code: a task killed at its memory
		// limit is no answer, whatever it exited with. Asked again while
		// the backend does not answer, as the output is.
		var obs execution.TaskObservation
		if err := a.tasks.whileUnreachable(ctx, operation, func(ctx context.Context) error {
			var err error
			obs, err = a.tasks.Driver.Observe(ctx, ref)
			return err
		}); err != nil {
			outcome = execution.Outcome{Termination: execution.Exited, ExitCode: code, CleanupErr: fmt.Errorf("reading how the task ended: %w", err)}
		} else if obs.OOMKilled {
			outcome = execution.Outcome{Termination: execution.OutOfMemory, ExitCode: -1,
				RunErr: fmt.Errorf("the script ran out of memory at its limit of %d MiB; raise apptownd -task-memory-mib if it needs more", attempt.Admitted.Limits.MemoryBytes>>20)}
		}
	}
	stopped := true
	if timedOut {
		stopped = a.tasks.stopQuietly(ctx, attempt, ref)
		outcome = execution.Outcome{Termination: execution.TimedOut, TimedOut: true, ExitCode: -1}
	}
	readOutput := func() error {
		return a.tasks.whileUnreachable(ctx, operation, func(ctx context.Context) error {
			page, err := a.tasks.Driver.Output(ctx, ref, execution.OutputRead{Max: maxTaskOutput})
			outcome.Output = page.Data
			return err
		})
	}
	if a.tasks.Remote == nil {
		if err := readOutput(); err != nil {
			a.tasks.failTask(task, attempt, err)
			return
		}
	} else {
		// The result is kept before anything else, its output included. An
		// output that cannot be read after this fails the step, with the
		// candidate kept. A checked landing's result is kept, not published.
		var publish func(repositoryedit.Candidate) (repositoryedit.Candidate, error)
		if plan.candidate == "" {
			publish = func(c repositoryedit.Candidate) (repositoryedit.Candidate, error) {
				return repositoryedit.PublishTaskResult(ctx, a.db, a.cfg.DataDir, a.branches, a.publisher, task.Run(), task.Step(), plan.folder, c, a.now())
			}
		}
		candidate, published, ok := a.tasks.keepAndPublish(task, attempt, plan.repository, func(c repositoryedit.Candidate) bool {
			if err := readOutput(); err != nil {
				a.tasks.failTask(task, attempt, fmt.Errorf("%w; its result is kept, unpublished, at %s (commit %s)", err, c.Ref, c.Tip))
				return false
			}
			return true
		}, publish)
		if !ok {
			return
		}
		plan.remote = &remoteTask{source: attempt.Source, candidate: candidate, published: published}
	}
	started := attempt.Deadline.Add(-attempt.Admitted.Limits.WallClock) // when the attempt was admitted
	err := a.tasks.settleRepeatedly(task, func() error {
		return a.settleScript(task, task.Step(), task.Run(), plan, outcome, a.now().Sub(started))
	})
	if err != nil && !errors.Is(err, workflow.ErrStepAttemptLost) {
		// The step's outcome is not recorded. The task and its record stay as
		// they are, so a restarted AppTown collects it again.
		log.Printf("task %s: settling its step: %v; it stays recorded for recovery", operation, err)
		return
	}
	if !stopped {
		// The timeout is recorded, but its stop is not confirmed: the attempt
		// stays unsettled, with its stop, so a restarted AppTown carries it out.
		log.Printf("task %s: its stop at the deadline is not confirmed; it stays recorded until it is", operation)
		return
	}
	a.tasks.settleTask(ctx, attempt, ref, &code, "")
}

// errTaskGone: the backend says the task does not exist. It is not
// evidence that nothing ran.
var errTaskGone = errors.New("the task's container is gone")

// errPastDeadline: the attempt's wall-clock budget ended before its task
// was started; it is not started now.
var errPastDeadline = errors.New("the task's deadline passed before it started")

// settleRepeatedly records a step's outcome with the shared bound: the
// runner's tries, its poll interval as the first wait; the step's context
// ending stops the tries.
func (r *TaskRunner) settleRepeatedly(task *workflow.Execution, settle func() error) error {
	return workflow.Settlement{Tries: r.SettleTries, Wait: r.poll()}.Retry(task.Context(), settle)
}

// provide returns the attempt's task on the backend: the recorded one, or
// the one ensured now under the attempt's key and recorded before it runs.
// Ensure refuses a different request under the key (ErrTaskConflict), and a
// request whose admission expired. Nothing is created after the attempt's
// deadline.
func (r *TaskRunner) provide(ctx context.Context, attempt tasks.Attempt, spec execution.TaskSpec) (string, error) {
	if attempt.ProviderID != "" {
		return attempt.ProviderID, nil
	}
	if !r.now().Before(attempt.Deadline) {
		return "", errPastDeadline
	}
	var obs execution.TaskObservation
	err := r.untilAnswered(ctx, attempt.Operation, attempt.Deadline, func(ctx context.Context) (err error) {
		obs, err = r.Driver.Ensure(ctx, spec)
		return err
	})
	if err != nil {
		return "", err
	}
	if r.abandon("created") {
		return "", errAbandoned
	}
	if err := r.Records.RecordProvider(ctx, attempt.Operation, obs.ID); err != nil {
		return "", err
	}
	return obs.ID, nil
}

// begin starts the task unless it has started, and settles what a lost or
// failed start reply means by observing the task: running or exited is
// started (and returned, so an exited task keeps its result); still created
// is started again (a driver starts a task at most once); absent is
// unknown. A task is not started after its attempt's deadline: its budget
// is spent, whatever grace its admission leaves for stopping it.
func (r *TaskRunner) begin(ctx context.Context, attempt tasks.Attempt) (execution.TaskObservation, error) {
	ref := execution.TaskRef{Key: attempt.Operation, ProviderID: attempt.ProviderID}
	// Before the deadline an unanswering backend is asked again until it.
	// After it (an AppTown that was down), the task gets one brief look: one
	// that exited in time keeps its result; one that does not answer is
	// given up and stopped, its outcome unknown. The budget is not renewed.
	observeUntil := attempt.Deadline
	if !r.now().Before(observeUntil) {
		observeUntil = r.now().Add(5 * r.poll())
	}
	for {
		var obs execution.TaskObservation
		err := r.untilAnswered(ctx, attempt.Operation, observeUntil, func(ctx context.Context) (err error) {
			obs, err = r.Driver.Observe(ctx, ref)
			return err
		})
		if err != nil {
			return obs, err
		}
		switch obs.State {
		case execution.TaskAbsent:
			return obs, fmt.Errorf("%w; its outcome is unknown", errTaskGone)
		case execution.TaskCreated:
			if !r.now().Before(attempt.Deadline) {
				return obs, errPastDeadline
			}
			if !r.now().Before(attempt.Admitted.Expires) {
				return obs, fmt.Errorf("%w: it had not started", execution.ErrExpired)
			}
			startCtx, cancel := r.waitUntil(ctx, attempt.Deadline)
			err := r.Driver.Start(startCtx, ref)
			cancel()
			if errors.Is(err, execution.ErrStartRefused) {
				// Refused with its reason (its private Docker could not be
				// made); nothing of it runs.
				return obs, err
			}
			if err != nil {
				log.Printf("task %s: starting it: %v; observing what happened", attempt.Operation, err)
				if err := poll.Wait(ctx, r.poll()); err != nil {
					return obs, err
				}
			}
			continue
		}
		if attempt.Observed == tasks.Reserved {
			if err := r.Records.Start(ctx, attempt.Operation); refusedByRecord(err) {
				return obs, fmt.Errorf("recording its start: %w", err)
			} else if err != nil {
				log.Printf("task %s: recording its start: %v", attempt.Operation, err)
			}
		}
		if r.abandon("started") {
			return obs, errAbandoned
		}
		return obs, nil
	}
}

// awaitTask waits for the task's exit until the attempt's deadline. It
// returns why it stopped waiting: "exited", "timed out", or what watch
// said, asked every poll: "stop" when a stop was recorded for this attempt,
// or "lost" when the step no longer belongs to it (stepOwnership). A backend
// that does not answer is asked again until the deadline.
func (r *TaskRunner) awaitTask(ctx context.Context, attempt tasks.Attempt, ref execution.TaskRef, watch func(context.Context) string) (int, string, error) {
	waitCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	type exited struct {
		code int
		err  error
	}
	done := make(chan exited, 1)
	go func() {
		var code int
		err := r.untilAnswered(waitCtx, attempt.Operation, attempt.Deadline, func(ctx context.Context) (err error) {
			code, err = r.Driver.Wait(ctx, ref)
			return err
		})
		done <- exited{code, err}
	}()
	ticker := time.NewTicker(r.poll())
	defer ticker.Stop()
	deadline := time.NewTimer(attempt.Deadline.Sub(r.now()))
	defer deadline.Stop()
	for {
		select {
		case e := <-done:
			if e.err != nil && !r.now().Before(attempt.Deadline) {
				return -1, "timed out", nil
			}
			return e.code, "exited", e.err
		case <-deadline.C:
			return -1, "timed out", nil
		case <-ticker.C:
			if why := watch(ctx); why != "" {
				return -1, why, nil
			}
		}
	}
}

// stepOwnership says whether waiting should end: "lost" when the attempt no
// longer owns its step (Execution.Current: its run ended, as when it was
// abandoned, its stage moved, or the step no longer runs at this attempt),
// "stop" when a stop was recorded for it, "" otherwise. A read failure keeps
// waiting: it is not permission to stop someone's work.
func stepOwnership(ctx context.Context, task *workflow.Execution, records *tasks.Records, operation string) string {
	if err := task.Current(); errors.Is(err, workflow.ErrStepAttemptLost) {
		return "lost"
	} else if err != nil {
		return ""
	}
	if attempt, err := records.Get(ctx, operation); err == nil && attempt.StopWanted {
		return "stop"
	}
	return ""
}

// stopAndFail stops the attempt's task (if it is this attempt's), records
// the step's failure, and then settles the attempt and removes the task. If
// the failure cannot be recorded, the stopped task and its record stay for a
// restarted AppTown to finish.
func (r *TaskRunner) stopAndFail(task *workflow.Execution, attempt tasks.Attempt, reason string) {
	ref, ours, stopped := r.stopOwn(task.Context(), attempt)
	if !r.failStep(task, attempt.Operation, reason) {
		return
	}
	switch {
	case !ours:
		r.settleNothingHeld(task.Context(), attempt, "a different task holds its key; it was left alone")
	case stopped:
		r.settleTask(task.Context(), attempt, ref, nil, reason)
	}
}

// stopTask stops and removes the attempt's task and settles the attempt,
// for an attempt whose step no longer belongs to it: whatever the task did
// before stopping is not a result. A task that cannot be stopped stays
// recorded, with its stop, for a restarted AppTown.
func (r *TaskRunner) stopTask(ctx context.Context, attempt tasks.Attempt, why string) {
	ref, ours, stopped := r.stopOwn(ctx, attempt)
	switch {
	case !ours:
		r.settleNothingHeld(ctx, attempt, "a different task holds its key; it was left alone")
	case stopped:
		r.settleTask(ctx, attempt, ref, nil, why)
	}
}

// StopStep stops a step attempt's tasks that have not settled (an agent's
// invocations): each stop is recorded before the backend is asked, so the
// attempt's own runner ends the step. found is false when the attempt has no
// task. An error names each task whose stop the backend did not confirm; it
// stays recorded with its stop.
func (r *TaskRunner) StopStep(ctx context.Context, stepID ids.ID, attempt int64) (stopped []string, found bool, err error) {
	invocations, err := r.Records.Invocations(ctx, stepID, attempt)
	if err != nil {
		return nil, false, err
	}
	var unconfirmed []string
	for _, inv := range invocations {
		if inv.Observed == tasks.Settled {
			continue
		}
		if inv.Backend == r.backend() {
			if _, ours, ok := r.stopOwn(ctx, inv); ok && ours {
				stopped = append(stopped, inv.Container)
				continue
			}
		}
		unconfirmed = append(unconfirmed, inv.Operation)
	}
	if len(unconfirmed) > 0 {
		err = fmt.Errorf("the stop of %s is recorded, but its backend has not confirmed it", strings.Join(unconfirmed, ", "))
	}
	return stopped, len(invocations) > 0, err
}

// stopOwn records the stop and stops the attempt's task. A task found only
// by the attempt's key is stopped only if it was made for the attempt's
// request: another's task is never touched (ours is false). stopped is
// whether the task is now stopped or gone.
func (r *TaskRunner) stopOwn(ctx context.Context, attempt tasks.Attempt) (ref execution.TaskRef, ours, stopped bool) {
	ctx, cancel := briefly(ctx)
	defer cancel()
	ref = execution.TaskRef{Key: attempt.Operation, ProviderID: attempt.ProviderID}
	// A stop already recorded is not written again: the task is stopped
	// whether or not a write would succeed now. A stop not yet recorded is
	// recorded first; if that fails, the task is still stopped, and the
	// attempt is left for a restarted AppTown to settle.
	durable := attempt.StopWanted
	if !durable {
		err := r.Records.RequestStop(ctx, attempt.Operation)
		durable = err == nil || errors.Is(err, tasks.ErrSettled)
		if !durable {
			log.Printf("task %s: recording the stop: %v", attempt.Operation, err)
		}
	}
	if attempt.ProviderID == "" {
		obs, err := r.Driver.Observe(ctx, ref)
		switch {
		case err != nil:
			log.Printf("task %s: finding it to stop it: %v; it stays recorded", attempt.Operation, err)
			return ref, true, false
		case obs.State == execution.TaskAbsent:
			return ref, true, true
		case obs.Request != attempt.Digest:
			return ref, false, false
		}
		ref.ProviderID = obs.ID
	}
	if err := r.Driver.Stop(ctx, ref); err != nil {
		log.Printf("task %s: stopping it: %v; it stays recorded", attempt.Operation, err)
		return ref, true, false
	}
	return ref, true, durable
}

// stopQuietly stops a task of this attempt whose outcome is being given up
// (timed out, unanswered), without settling anything. It reports whether the
// stop was confirmed.
func (r *TaskRunner) stopQuietly(ctx context.Context, attempt tasks.Attempt, ref execution.TaskRef) bool {
	ctx, cancel := briefly(ctx)
	defer cancel()
	if err := r.Driver.Stop(ctx, ref); err != nil {
		log.Printf("task %s: stopping it: %v", attempt.Operation, err)
		return false
	}
	return true
}

// refuse fails the step of an attempt the backend refused before creating
// anything (another request's task holds its key, or the request cannot run
// there), and settles the attempt, which made nothing. Another request's
// task is not touched.
//
// The refusal is recorded on the attempt first. Until then a restart asks
// the backend again, which refuses again; from then on a restart finishes
// the step and settles the attempt from the record alone (settleRefused).
func (r *TaskRunner) refuse(task *workflow.Execution, attempt tasks.Attempt, cause error) {
	var refused error
	if err := r.settleRepeatedly(task, func() error {
		refused = r.Records.Refuse(context.WithoutCancel(task.Context()), attempt.Operation, cause.Error())
		if refusedByRecord(refused) {
			return nil // decided: tried again, it is refused again
		}
		return refused
	}); err != nil {
		log.Printf("task %s: recording its refusal: %v; it stays recorded for recovery", attempt.Operation, err)
		return
	}
	if refused != nil {
		r.endAsRecorded(task, attempt, refused)
		return
	}
	attempt.Observed, attempt.Detail = tasks.Refused, cause.Error()
	r.settleRefused(task.Context(), task, attempt)
}

// settleRefused fails the step of a refused attempt, if it is still the
// attempt's, then settles the attempt. A step that moved on needs nothing.
func (r *TaskRunner) settleRefused(ctx context.Context, task *workflow.Execution, attempt tasks.Attempt) {
	if task != nil && !r.failStep(task, attempt.Operation, "running the step's task: "+attempt.Detail) {
		return
	}
	r.settleNothingHeld(ctx, attempt, attempt.Detail)
}

// settleTask marks the attempt settled, then removes its task. A release
// the backend does not confirm is tried again later (ReleaseSettled).
func (r *TaskRunner) settleTask(ctx context.Context, attempt tasks.Attempt, ref execution.TaskRef, code *int, why string) {
	ctx = context.WithoutCancel(ctx) // the settlement of a task that ran
	if err := r.Records.Settle(ctx, attempt.Operation, code, why); err != nil && !errors.Is(err, tasks.ErrSettled) {
		log.Printf("task %s: settling its record: %v", attempt.Operation, err)
		return
	}
	r.release(ctx, attempt.Operation, ref)
}

// settleNothingHeld settles an attempt of which nothing is held on the
// backend (refused before anything was created, or another request's task
// holds its key): nothing is released under its key.
func (r *TaskRunner) settleNothingHeld(ctx context.Context, attempt tasks.Attempt, why string) {
	ctx = context.WithoutCancel(ctx) // a settlement
	if err := r.Records.SettleNothingHeld(ctx, attempt.Operation, why); err != nil && !errors.Is(err, tasks.ErrSettled) {
		log.Printf("task %s: settling its record: %v", attempt.Operation, err)
		return
	}
	r.dropRequest(attempt.Operation)
}

// failTask settles the step as an infrastructure error and records the
// attempt's outcome as unknown: the task may or may not have run. If the
// step's outcome cannot be recorded, the attempt stays as it was.
func (r *TaskRunner) failTask(task *workflow.Execution, attempt tasks.Attempt, cause error) {
	if !r.failStep(task, attempt.Operation, "running the step's task: "+cause.Error()) {
		return
	}
	if err := r.Records.RecordUnknown(context.WithoutCancel(task.Context()), attempt.Operation, cause.Error()); err != nil && !errors.Is(err, tasks.ErrSettled) {
		log.Printf("task %s: recording its unknown outcome: %v", attempt.Operation, err)
	}
}

// notKept is how a held attempt's reason reads to people: in its step's
// reason, and in the log a restarted AppTown writes for it
// (design/operations/workers.md, "Held workspaces").
func notKept(reason string) string { return "result not kept: " + reason }

// holdResult records the attempt held (tasks.Hold: its workspace on the
// worker is the only copy of its work, so its task is never released, even
// by a restarted AppTown after its step has moved on), and only then the step
// as an error. The hold is durable
// before the step ends: if it cannot be recorded, the step stays running at
// this attempt and the attempt as it was (collecting), so a restarted AppTown
// collects the result again, or holds it (Resume). A crash between the two
// leaves a held attempt whose step Resume finishes.
func (r *TaskRunner) holdResult(task *workflow.Execution, attempt tasks.Attempt, cause error) {
	detail := notKept(cause.Error())
	var refused error
	err := r.settleRepeatedly(task, func() error {
		refused = r.Records.Hold(context.WithoutCancel(task.Context()), attempt.Operation, cause.Error())
		if refusedByRecord(refused) {
			return nil // decided: tried again, it is refused again
		}
		return refused
	})
	if err != nil {
		log.Printf("task %s: %s; recording its hold failed: %v; its step stays running for a restarted AppTown", attempt.Operation, detail, err)
		return
	}
	if refused != nil {
		r.endAsRecorded(task, attempt, refused)
		return
	}
	if r.abandon("held") {
		return
	}
	r.failStep(task, attempt.Operation, "running the step's task: "+detail) // it stays held either way
}

// failStep records the step's outcome as an error with reason, tried again
// with the shared bound. It reports whether the step is finished: recorded,
// or no longer this attempt's. When it is not, the caller leaves the attempt
// recorded for a restarted AppTown.
func (r *TaskRunner) failStep(task *workflow.Execution, operation, reason string) bool {
	err := r.settleRepeatedly(task, func() error {
		return task.Finish(workflow.EncodeOutcome(workflow.StatusError, map[string]any{"reason": reason}, nil))
	})
	if err != nil && !errors.Is(err, workflow.ErrStepAttemptLost) {
		log.Printf("task %s: recording its step's error (%s): %v; it stays recorded for recovery", operation, reason, err)
		return false
	}
	return true
}

// refusedByRecord reports whether Tasks refused a transition because the
// attempt's record had already moved on (settled, held, or another state
// the transition does not follow). Such a refusal is a decision, not a
// failed write: it is never tried again or read as success.
func refusedByRecord(err error) bool {
	return errors.Is(err, tasks.ErrSettled) || errors.Is(err, tasks.ErrHeld) || errors.Is(err, tasks.ErrNotAccepted)
}

// endAsRecorded ends a path whose transition Tasks refused (cause): the
// attempt's record, read again, is what the step becomes. Held: the step is
// an error and the work stays held. Settled: nothing more. Refused: the
// step fails and the attempt settles with nothing held. Anything else: the
// step is an error and the outcome unknown, never a success.
func (r *TaskRunner) endAsRecorded(task *workflow.Execution, attempt tasks.Attempt, cause error) {
	ctx := context.WithoutCancel(task.Context())
	current, err := r.Records.Get(ctx, attempt.Operation)
	if err != nil {
		log.Printf("task %s: %v; reading its record: %v; it stays recorded for recovery", attempt.Operation, cause, err)
		return
	}
	switch current.Observed {
	case tasks.Settled:
	case tasks.Held:
		r.failStep(task, current.Operation, "running the step's task: "+notKept(current.Detail))
	case tasks.Refused:
		r.settleRefused(ctx, task, current)
	default:
		r.failTask(task, current, fmt.Errorf("its record refused the outcome (%w); its outcome is unknown", cause))
	}
}

// resultKept reports whether a remote attempt's result is kept in the
// project's repository, as its candidate or its refused ref
// (repositoryedit.FindCandidate). Only then may its workspace be released.
func (a *Commands) resultKept(ctx context.Context, attempt tasks.Attempt) (bool, error) {
	source, err := workflow.ReadSourceIn(ctx, a.db, workflow.RunRef{ID: attempt.Owner.RunID, ProjectID: attempt.Owner.ProjectID})
	if err != nil {
		return false, err
	}
	hub, err := source.Repository(a.cfg.DataDir)
	if err != nil {
		return false, err
	}
	_, found, err := repositoryedit.FindCandidate(ctx, hub, attempt.Owner.StepID, attempt.Owner.Attempt, attempt.Source)
	return found, err
}

// finish records a step outcome for a task never reserved.
func (a *Commands) finish(task *workflow.Execution, status workflow.StepState, reason string) {
	if err := task.Settle(workflow.EncodeOutcome(status, map[string]any{"reason": strings.TrimSpace(reason)}, nil)); err != nil {
		log.Printf("step %d: recording its outcome: %v", task.Step().ID, err)
	}
}
