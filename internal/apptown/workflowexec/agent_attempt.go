package workflowexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	piharness "github.com/flokkit/apptown/internal/apptown/agentharness/pi"
	"github.com/flokkit/apptown/internal/apptown/repositoryedit"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/tasks"
	"github.com/flokkit/apptown/internal/apptown/usage"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/strs"
)

// An agent step attempt on a worker (design/operations/workers.md, "Agent
// and review steps on the worker"). The attempt owns one
// workspace on the worker, step-<id>-a<attempt>, made from the attempt's
// source commit. Each run of Pi in it is one invocation, recorded by the
// tasks owner and driven by the same TaskRunner scripts use, one at a time:
// the runner knows nothing of the sequence. The sequence is
// nextAgentInvocation: "agent", then at most one "agent-retry" after an
// empty turn, reserved only once the worker reported the first exited.
//
// After each invocation exits, its whole output is read (usage, recorded
// once per invocation), and its declared result files (the transcript,
// stored and recorded before anything later runs in the session; the step's
// output file). After the last, the attempt's commits are collected once as
// its candidate in the hub, the output and written files are read from the
// candidate and the result files, the candidate is published (or kept
// refused), and the outcome is recorded with its artifacts in one
// transaction. Then the invocations are released, then the workspace.
//
// A restarted AppTown continues an attempt from its newest invocation's
// recorded state (RecoverAgent). An invocation whose outcome is unknown (a
// stop the worker did not confirm) holds the workspace, and the attempt is
// an error.

// The invocations of an agent or review step's attempt.
const (
	agentInvocation        = "agent"
	agentRetryInvocation   = "agent-retry"
	reviewInvocation       = "review"
	reviewWrapUpInvocation = "review-wrapup"
	reviewFixUpInvocation  = "review-fixup"
)

// nextAgentInvocation is an agent step's sequence: what runs after done,
// from its recorded facts, or "" when the attempt's invocations are over.
// Only the first invocation's empty turn earns one more.
func nextAgentInvocation(done string, emptyTurn bool) string {
	if done == agentInvocation && emptyTurn {
		return agentRetryInvocation
	}
	return ""
}

// nextInvocation is the attempt's sequence for either kind of step: a
// review's is its verdict table (reviewStep).
func nextInvocation(done string, res invocationResult) string {
	if done == reviewInvocation {
		_, next, _ := reviewStep(done, res)
		return next
	}
	return nextAgentInvocation(done, res.emptyTurn())
}

// followUp is the one extra invocation an attempt may earn: the line the
// step's feed shows, the event recorded (if any), and for a review the
// order its session is resumed with and its fixed allowance, recorded when
// it is reserved. An agent's retry sends the admitted request again, with
// the step's own timeout.
type followUp struct {
	note, event string
	order       string
	allowance   time.Duration
}

var followUps = map[string]followUp{
	agentRetryInvocation:   {note: "⟳ empty model output — retrying", event: "empty model output"},
	reviewWrapUpInvocation: {note: "⏸ no verdict yet — resuming session to write it", order: reviewWrapUp, allowance: 5 * time.Minute},
	reviewFixUpInvocation:  {note: "⏸ verdict file invalid — resuming session to rewrite it", order: reviewFixUp, allowance: 4 * time.Minute},
}

// agentOperation is an invocation's operation key: its task's key on the
// worker, and its record's.
func agentOperation(stepID ids.ID, attempt int64, invocation string) string {
	return fmt.Sprintf("step-%d-attempt-%d-%s", stepID, attempt, invocation)
}

// agentWorkspace is the workspace an attempt's invocations share.
func agentWorkspace(stepID ids.ID, attempt int64) string {
	return fmt.Sprintf("step-%d-a%d", stepID, attempt)
}

// agentAdmission is what AppTown keeps beside an invocation's admitted
// request: what the request was built from, which its settlement reports.
// A recovered invocation settles with these, never with settings read again.
type agentAdmission struct {
	Invocation string `json:"invocation"`
	// Number counts the attempt's invocations from 1: the transcript's
	// record orders captures by it.
	Number       int64                `json:"number"`
	Session      string               `json:"session"`
	Image        execution.AgentImage `json:"image"`
	Model        string               `json:"model"`
	ModelSlot    string               `json:"modelSlot"`
	ModelProfile string               `json:"modelProfile"`
	Steering     struct {
		Hash, GuidanceHash string
		Skills, Resources  map[string]int
	} `json:"steering"`
	// Delegated: the steering promised subagents.
	Delegated bool `json:"delegated,omitempty"`
	// PrivateDocker is what the invocation's private Docker rests on; ""
	// for none.
	PrivateDocker string `json:"privateDocker,omitempty"`
	// Profile is the worker's profile it was admitted on.
	Profile *execution.WorkerProfile `json:"profile,omitempty"`
	// Dispatches is the step's dispatch count, the evidence's attempts.
	Dispatches int           `json:"dispatches"`
	Timeout    time.Duration `json:"timeout"`
	// Began is when the attempt's first invocation was admitted.
	Began    time.Time `json:"began"`
	QueuedMs int64     `json:"queuedMs"`
	// Review is set for a review step's invocations: what its verdict is
	// judged against.
	Review *reviewAdmission `json:"review,omitempty"`
}

// reviewAdmission is what a review's verdict is judged against, as it was
// admitted: the report kind it audits and the implementer's whole claim
// (the scope guard's unclaimed rule reads it, never the pointer a large
// claim's brief carries).
type reviewAdmission struct {
	Of    string `json:"of"`
	Claim string `json:"claim"`
}

// workerAttempt is an agent step attempt running on the worker, as its
// invocations are driven and settled.
type workerAttempt struct {
	task   *workflow.Execution
	step   *workflow.Step // claimed
	run    *workflow.Run
	source workflow.Source
	cfg    agentConfig
	folder *gitexec.Folder // the run's folder, locked: publication brings it forward
	obs    *stepAttempt
}

// runOnWorker runs an agent step's attempt, or a review step's (review is
// set), on AppTown's worker. A step the worker cannot run is refused
// before any effect: nothing is published, rewound or sent. It never falls
// back to AppTown's own machine. The attempt's source is found without
// publishing it (a restart's is the base); a review's brief is read from the
// hub at that commit (the change's diff and scope to it);
// and what the step needs and the worker's image promises are checked
// there, before anything is published or rewound.
func (a *Executor) runOnWorker(task *workflow.Execution, attempt *stepAttempt, review *reviewCall, queuedMs int64, fail func(workflow.StepState, map[string]any)) {
	step, run := task.Step(), task.Run()
	ctx := context.WithoutCancel(task.Context())
	ref := workflow.RunRef{ID: run.ID, ProjectID: run.ProjectID}
	source, err := workflow.ReadSourceIn(ctx, a.db, ref)
	if err != nil {
		// Unclaimed, the step stays pending for the dispatcher to retry.
		attempt.settle("unclaimed")
		return
	}
	// Once the attempt has settled, its scratch ref goes with its token.
	defer a.dropSettledScratch(ctx, source)
	// Publication brings the run's folder forward: it is locked for the
	// attempt, as for a script, and waited for before the claim.
	folder := gitexec.LockFolder(source.Workspace(a.cfg.DataDir))
	defer folder.Release()
	claim, err := task.Claim()
	if err != nil {
		attempt.settle("unclaimed")
		return
	}
	*step = *claim
	attempt.claimed(claim.Attempt)
	refuse := func(err error) { fail(workflow.StatusError, map[string]any{"reason": err.Error()}) }
	var cfg agentConfig
	if err := workflow.DecodeStepConfig(step.Config, &cfg); err != nil {
		refuse(err)
		return
	}
	call := workerAgentCall{run: run, step: step, source: source, invocation: agentInvocation, review: review}
	mode, err := workflow.ParseRetryMode(cfg.RetryMode)
	history := "agent session history"
	if review != nil {
		call.invocation, mode, err, history = reviewInvocation, workflow.RetryDefault, nil, "the review's session"
	}
	if err != nil {
		refuse(err)
		return
	}
	session, err := workflow.AgentSessionIn(ctx, a.db, ref, step.ID, mode)
	if err != nil {
		refuse(fmt.Errorf("reading %s: %w", history, err))
		return
	}
	call.session = session.ID
	restart := mode == workflow.RetryRestart && source.RepoBacked() && source.RunBaseSHA != ""
	// A continued session is given exactly the history its earlier
	// attempt's receipt names; history that cannot be given back refuses
	// the attempt, never starting it afresh.
	if review == nil {
		if call.transcript, err = a.earlierHistory(ctx, run, step, session.ID); err != nil {
			refuse(err)
			return
		}
	}
	call.commit = source.RunBaseSHA
	var proposed repositoryedit.TaskSource
	if !restart && (source.RepoBacked() || review != nil) {
		if proposed, err = repositoryedit.ProposeTaskSource(ctx, a.cfg.DataDir, source); err != nil {
			refuse(err)
			return
		}
		defer proposed.Discard(ctx)
		call.commit = proposed.Commit
	}
	// AppTown merges main in first when the branch is behind it
	// (main_merge.go): the step is built on the merge, which is published
	// after the step's own source, once nothing can refuse the step.
	from := call.commit
	var mainMerge scm.MainMerge
	if review == nil {
		if mainMerge = a.mergeMainFirst(ctx, run, step, step.SourceGeneration(), source, call.commit); mainMerge.Commit != "" {
			call.commit = mainMerge.Commit
		}
	}
	if review != nil {
		hub := source.Repository(a.cfg.DataDir)
		diff, err := hubReviewDiff(ctx, hub, source, call.commit)
		if err == nil {
			review.brief, err = a.reviewBrief(ctx, run, source, review.cfg, diff, hubScope(hub, source, call.commit))
		}
		if err != nil {
			refuse(err)
			return
		}
	}
	call.at = a.worker.Tasks.now()
	req, err := a.prepareAgentOnWorker(ctx, call)
	if err != nil {
		refuse(err)
		return
	}
	if restart {
		// The folder is reset, then the branch rewound, in the hub: the
		// attempt's source is the base.
		err = a.restartFromBase(ctx, run, step, source, folder)
	} else {
		_, err = proposed.Publish(ctx, a.branches, run, step)
	}
	if err == nil && mainMerge.Commit != "" {
		err = repositoryedit.PublishMainMerge(ctx, a.db, a.cfg.DataDir, a.branches, a.publisher, run, step, folder, from, mainMerge, a.now())
	}
	if err != nil {
		refuse(err)
		return
	}
	meta := agentAdmission{Invocation: call.invocation, Session: session.ID, Dispatches: session.Dispatches, QueuedMs: queuedMs}
	if review != nil {
		meta.Review = &reviewAdmission{Of: review.cfg.Of, Claim: review.brief.claim}
	} else {
		meta.Delegated = piharness.DelegationWith(piharness.MapLabels(req.image.Labels)) != ""
	}
	a.startAttempt(ctx, &workerAttempt{task: task, step: step, run: run, source: source, cfg: cfg, folder: folder, obs: attempt}, call, req, meta, fail)
}

// startAttempt reserves an attempt's first invocation, with what its
// request was built from, and drives the attempt from there.
func (a *Executor) startAttempt(ctx context.Context, w *workerAttempt, call workerAgentCall, req workerAgentRequest, meta agentAdmission, fail func(workflow.StepState, map[string]any)) {
	meta.Number, meta.Image, meta.Began, meta.PrivateDocker, meta.Profile = 1, req.image, call.at, req.privateDocker, req.profile
	meta.Model, meta.ModelSlot, meta.ModelProfile = req.model, req.modelSlot, req.modelProfile
	meta.Timeout = req.spec.Admission.Limits.WallClock
	meta.Steering.Hash, meta.Steering.GuidanceHash, meta.Steering.Skills, meta.Steering.Resources = req.steering.Hash, req.steering.GuidanceHash, req.steering.Skills, req.steering.Resources
	inv, kept, err := a.reserveInvocation(ctx, w.step, w.run, req.spec, call.commit, meta, call.at.Add(meta.Timeout))
	if err != nil {
		if inv.Operation == "" {
			fail(workflow.StatusError, map[string]any{"reason": "recording the step's invocation: " + err.Error()})
		} else {
			a.worker.Tasks.stopAndFail(w.task, inv, "keeping the invocation's request: "+err.Error())
			a.worker.Tasks.releaseAttempt(ctx, inv)
		}
		return
	}
	a.recordSteering(ctx, w.step, req.steering)
	w.obs.run()
	a.progress.Start(w.step.ID)
	defer workflow.CaptureProgress(ctx, a.db, a.progress, w.step.ID, a.clock)
	a.continueInvocation(ctx, w, inv, kept)
}

// reserveInvocation records an invocation, with its deadline, then keeps its
// admitted request, before anything is asked of the worker. A reservation
// found again finds its request kept too. The returned attempt's Operation
// is empty when nothing was recorded.
func (a *Executor) reserveInvocation(ctx context.Context, step *workflow.Step, run *workflow.Run, spec execution.TaskSpec, source string, meta agentAdmission, deadline time.Time) (tasks.Attempt, admittedRequest, error) {
	r := a.worker.Tasks
	spec.Request = ""
	spec.Request = spec.Digest()
	inv, err := r.Records.Reserve(ctx, tasks.Admission{Operation: spec.Key, Invocation: meta.Invocation, Workspace: spec.SharedWorkspace,
		Owner:   tasks.Owner{ProjectID: run.ProjectID, RunID: run.ID, StepID: step.ID, Attempt: step.OwnedAttempt()},
		Backend: r.backend(), Container: spec.Key, Digest: spec.Request, Source: source, Admitted: spec.Admission, Deadline: deadline})
	if err != nil {
		return tasks.Attempt{}, admittedRequest{}, err
	}
	kept, err := r.keepRequest(inv.Operation, admittedRequest{Task: spec, Agent: &meta})
	if err == nil && (kept.Task.Request != inv.Digest || kept.Agent == nil) {
		err = errors.New("the kept request is not the one admitted")
	}
	return inv, kept, err
}

// RecoverAgent continues, in the workflow's dispatch, the agent step attempt
// a previous AppTown process ran on the worker: from its newest invocation's
// recorded state (waiting for it, collecting it, or settling the attempt).
// An attempt whose step moved on is stopped and released, never settled.
func (a *Executor) RecoverAgent(task *workflow.Execution) {
	defer reportPanic(a.panics, "RecoverAgent")
	step, run := task.Step(), task.Run()
	// The task lives by its durable record, not a context: what a stopped
	// AppTown leaves running is collected on restart.
	ctx := context.WithoutCancel(task.Context())
	if a.worker == nil || a.worker.Tasks == nil {
		log.Printf("run %d step %d: its attempt ran on a worker, and this AppTown has none; it stays running", run.ID, step.ID)
		return
	}
	r := a.worker.Tasks
	invocations, err := r.Records.Invocations(ctx, step.ID, step.OwnedAttempt())
	if err != nil || len(invocations) == 0 {
		log.Printf("run %d step %d: reading its recorded invocations: %v (%d); it stays running", run.ID, step.ID, err, len(invocations))
		return
	}
	newest := invocations[len(invocations)-1]
	source, err := workflow.ReadSourceIn(ctx, a.db, workflow.RunRef{ID: run.ID, ProjectID: run.ProjectID})
	if err != nil {
		log.Printf("run %d step %d: reading its source: %v; its invocation stays recorded", run.ID, step.ID, err)
		return
	}
	defer a.dropSettledScratch(ctx, source)
	folder := gitexec.LockFolder(source.Workspace(a.cfg.DataDir))
	defer folder.Release()
	// It may have waited for the folder: the attempt must still be current.
	// Only a confirmed loss stops its invocation.
	if err := task.Current(); errors.Is(err, workflow.ErrStepAttemptLost) {
		r.stopTask(ctx, newest, "its step's attempt ended while it waited for the folder")
		r.releaseAttempt(ctx, newest)
		return
	} else if err != nil {
		log.Printf("run %d step %d: checking its attempt: %v; its invocation stays recorded", run.ID, step.ID, err)
		return
	}
	// A later invocation was reserved only after the earlier ones exited
	// and were read: any left unsettled by a crash are settled now.
	for _, earlier := range invocations[:len(invocations)-1] {
		if earlier.Observed != tasks.Settled {
			if err := r.Records.Settle(ctx, earlier.Operation, earlier.ExitCode, "followed by "+newest.Invocation); err != nil && !errors.Is(err, tasks.ErrSettled) {
				log.Printf("task %s: settling it: %v", earlier.Operation, err)
			}
		}
	}
	kept, found, err := r.storedRequest(newest.Operation)
	switch {
	case err != nil:
		r.stopAndFail(task, newest, "reading the invocation's kept request: "+err.Error())
		r.releaseAttempt(ctx, newest)
		return
	case !found || kept.Agent == nil || kept.Task.Request != newest.Digest:
		// Recorded without its request: nothing was sent, and nothing is
		// rebuilt from settings that may have changed since.
		r.stopAndFail(task, newest, "the invocation's admitted request is not kept")
		r.releaseAttempt(ctx, newest)
		return
	}
	var cfg agentConfig
	if err := workflow.DecodeStepConfig(step.Config, &cfg); err != nil {
		r.stopAndFail(task, newest, err.Error())
		r.releaseAttempt(ctx, newest)
		return
	}
	obs := a.obs.begin(ctx, run.ID, step.ID, step.Type, run.WorkflowName, run.ProjectID)
	defer obs.settle("abandoned")
	obs.claimed(step.OwnedAttempt())
	obs.run()
	a.progress.Start(step.ID)
	defer workflow.CaptureProgress(task.Context(), a.db, a.progress, step.ID, a.clock)
	a.continueInvocation(ctx, &workerAttempt{task: task, step: step, run: run, source: source, cfg: cfg, folder: folder, obs: obs}, newest, kept)
}

// continueInvocation takes an invocation from wherever it was recorded to
// exited, then reads it and either reserves the next invocation of the
// sequence or finishes the attempt. kept is its admitted request.
func (a *Executor) continueInvocation(ctx context.Context, w *workerAttempt, inv tasks.Attempt, kept admittedRequest) {
	r := a.worker.Tasks
	inv, code, timedOut, summarized, ok := r.execute(w.task, a.db, inv, kept.Task, taskKind{invocation: true, follow: func(ctx context.Context, ref execution.TaskRef) <-chan int64 {
		return a.followOutput(ctx, ref, w.step.ID)
	}})
	if !ok {
		return
	}
	operation := inv.Operation
	ref := execution.TaskRef{Key: operation, ProviderID: inv.ProviderID}
	if timedOut && !r.stopWithin(ctx, inv, ref) {
		// Whatever it does now is unknown: nothing more starts in its
		// workspace, which is held, and the attempt is an error.
		r.holdResult(w.task, inv, errors.New("the invocation timed out and the worker did not confirm its stop; its outcome is unknown"))
		return
	}
	res, err := a.readInvocation(ctx, w, inv, ref, *kept.Agent, code, timedOut, summarized)
	if err != nil {
		// Its history is recorded neither kept nor unavailable: nothing
		// more runs in the session, and the attempt is neither settled nor
		// released. The invocation stays collecting, and a restarted AppTown
		// reads it again and records its history (RecoverAgent).
		log.Printf("task %s: %v; it stays recorded for recovery", operation, err)
		return
	}
	next := nextInvocation(kept.Agent.Invocation, res)
	if next == agentRetryInvocation && kept.Task.Private != nil {
		// As on AppTown's host: an agent with private Docker is not run
		// again in its attempt; its daemon lived and ended with it.
		next = ""
	}
	if next != "" {
		if f := followUps[next]; f.event != "" {
			recordEvent(ctx, a.db, w.run.ID, "step.retried", map[string]any{"step": w.step.Name, "reason": f.event}, a.now())
		}
		a.progress.Append(w.step.ID, followUps[next].note)
		nextInv, nextKept, err := a.reserveNext(ctx, w, inv, kept, next)
		if err != nil {
			if nextInv.Operation == "" {
				// Not recorded: the earlier invocation stays collecting, and
				// a restarted AppTown reads it and decides again.
				log.Printf("task %s: reserving %s: %v; it stays recorded for recovery", operation, next, err)
				return
			}
			r.stopAndFail(w.task, nextInv, "keeping the invocation's request: "+err.Error())
			a.settleEarlier(ctx, inv, code, "followed by "+next)
			r.releaseAttempt(ctx, nextInv)
			return
		}
		// The decision is recorded (the next invocation's reservation);
		// the earlier invocation is read and done.
		a.settleEarlier(ctx, inv, code, "followed by "+next)
		a.continueInvocation(ctx, w, nextInv, nextKept)
		return
	}
	a.finishAttempt(ctx, w, inv, ref, res, code, *kept.Agent)
}

// settleEarlier settles an invocation a later one follows. Its task is
// released with the attempt's.
func (a *Executor) settleEarlier(ctx context.Context, inv tasks.Attempt, code int, why string) {
	ctx = context.WithoutCancel(ctx) // the settlement of an invocation that ran
	if err := a.worker.Tasks.Records.Settle(ctx, inv.Operation, &code, why); err != nil && !errors.Is(err, tasks.ErrSettled) {
		log.Printf("task %s: settling it: %v", inv.Operation, err)
	}
}

// reserveNext reserves the invocation that follows inv (followingRequest),
// recorded now and not renewed later.
func (a *Executor) reserveNext(ctx context.Context, w *workerAttempt, inv tasks.Attempt, kept admittedRequest, next string) (tasks.Attempt, admittedRequest, error) {
	now := a.worker.Tasks.now()
	spec, meta := followingRequest(kept, agentOperation(w.step.ID, w.step.OwnedAttempt(), next), next, now)
	return a.reserveInvocation(ctx, w.step, w.run, spec, inv.Source, meta, now.Add(meta.Timeout))
}

// followingRequest is the request of the invocation next, which follows an
// admitted one: the same request under its own key (for a review's wrap-up
// or fixup, its order as the message, the last argument, and its
// allowance), with its own budget from now. Nothing is read from settings.
func followingRequest(kept admittedRequest, key, next string, now time.Time) (execution.TaskSpec, agentAdmission) {
	meta := *kept.Agent
	meta.Invocation, meta.Number = next, meta.Number+1
	spec := kept.Task
	spec.Key = key
	if f := followUps[next]; f.order != "" {
		spec.Argv = append(append([]string{}, spec.Argv[:len(spec.Argv)-1]...), f.order)
		meta.Timeout, spec.Admission.Limits.WallClock = f.allowance, f.allowance
	}
	spec.Admission.Expires = now.Add(meta.Timeout + taskGrace)
	return spec, meta
}

// maxInvocationOutput bounds what is read of an invocation's output; more
// is not read, and its usage is recorded as incomplete.
const maxInvocationOutput = 64 << 20

// outputPage is how much of an invocation's output one read asks for.
const outputPage = 4 << 20

// followOutput streams an invocation's summary lines to the step's live
// feed while it runs, until ctx ends, from its log. It reads only whole
// lines and keeps nothing: the result is read from the invocation's events
// file once it exits. The channel carries how many events it summarized
// (the log holds stderr too, which the file does not, so the catch-up
// counts events, never bytes).
func (a *Executor) followOutput(ctx context.Context, ref execution.TaskRef, stepID ids.ID) <-chan int64 {
	done := make(chan int64, 1)
	driver := a.worker.Tasks.Driver
	go func() {
		var cursor, summarized int64
		var partial []byte
		defer func() { done <- summarized }()
		for {
			page, err := driver.Output(ctx, ref, execution.OutputRead{Cursor: cursor, Max: outputPage})
			if err == nil && len(page.Data) > 0 && page.Next > cursor && !page.Truncated {
				cursor = page.Next
				buf := append(partial, page.Data...)
				cut := bytes.LastIndexByte(buf, '\n')
				if cut >= 0 {
					a.summarize(stepID, buf[:cut+1])
					for _, line := range bytes.SplitAfter(buf[:cut+1], []byte("\n")) {
						if piharness.IsEvent(line) {
							summarized++
						}
					}
					partial = append([]byte(nil), buf[cut+1:]...)
				} else {
					partial = buf
				}
				if len(partial) > outputPage || cursor > maxInvocationOutput {
					return // a line too long to be a summary, or more than is read
				}
				continue
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(a.worker.Tasks.poll()):
			}
		}
	}()
	return done
}

// summarize appends the step's live feed with the summary of each line.
func (a *Executor) summarize(stepID ids.ID, lines []byte) {
	for _, line := range strings.Split(string(lines), "\n") {
		if s := piharness.SummarizeEvent(line); len(s) > 0 {
			a.progress.Append(stepID, s...)
		}
	}
}

// invocationResult is what an exited invocation left: its outcome and
// transcript read (piResult), and its declared result files.
type invocationResult struct {
	piResult
	// outputErr: its output could not be read, so its text and usage are
	// unknown.
	outputErr  error
	results    map[string]execution.ResultFile
	resultsErr error
}

// emptyTurn: the model answered nothing, with no timeout, no run error and
// no provider error, as far as its whole output says. An unread output is
// not an empty turn.
func (r invocationResult) emptyTurn() bool {
	return r.outputErr == nil && !r.TimedOut && r.RunErr == nil && r.ProviderError == "" && strings.TrimSpace(r.Text) == ""
}

// readInvocation reads an exited invocation: its declared result files,
// its events among them (read from that file, never its log: the log's end
// can be lost), its usage (recorded once, keyed by its operation, with its
// completeness), and its transcript (stored and recorded with the attempt,
// or recorded unavailable with why). Every part is safe to repeat after a
// restart. summarized is how many events the live feed already showed. An
// error says its history could not be recorded either way.
func (a *Executor) readInvocation(ctx context.Context, w *workerAttempt, inv tasks.Attempt, ref execution.TaskRef, meta agentAdmission, code int, timedOut bool, summarized int64) (invocationResult, error) {
	r := a.worker.Tasks
	var res invocationResult
	res.Outcome = execution.Outcome{Termination: execution.Exited, ExitCode: code}
	if code != 0 {
		// A failed process, as AppTown's own runner reports it: an error,
		// never an empty turn to retry (emptyTurn) or a pass.
		res.RunErr = fmt.Errorf("exit status %d", code)
	}
	if timedOut {
		res.Outcome = execution.Outcome{Termination: execution.TimedOut, TimedOut: true, ExitCode: -1}
	}
	res.Tries = int(meta.Number)
	res.resultsErr = r.whileUnreachable(ctx, inv.Operation, func(ctx context.Context) (err error) {
		driver, derr := a.worker.driver()
		if derr != nil {
			return derr
		}
		res.results, err = driver.Results(ctx, ref)
		return err
	})
	// The log is the failure's words (its stderr tail), never the result.
	res.Output = a.readLog(ctx, inv.Operation, ref)
	out, err := invocationEvents(res.results, res.resultsErr)
	if err != nil {
		// No usage is recorded: an unread output is unknown usage, never zero.
		// A task the worker says is gone took its output with it.
		if observed, oerr := r.Driver.Observe(ctx, ref); oerr == nil && observed.State == execution.TaskAbsent {
			err = fmt.Errorf("%w: %w", errTaskGone, err)
		}
		res.outputErr = err
	} else {
		res.Events = out
		a.summarize(w.step.ID, piharness.EventsAfter(out, summarized))
		res.Text, res.Model, res.Provider, res.Usage = piharness.ParseOutput(out)
		spent := sumPiUsage(out)
		incomplete := spent.Incomplete
		if incomplete == "" && meta.Delegated {
			// The pinned runtime's stream carries no usage for the
			// subagents the steering promised: their calls are not in it.
			incomplete = "delegated calls not included"
		}
		model, provider := spent.Model, spent.Provider
		if model == "" {
			model, provider = res.Model, res.Provider
		}
		source := w.source
		if err := a.ledger.RecordOnce(ctx, inv.Operation, usage.Entry{Kind: "step", ProjectID: source.ProjectID, RunID: source.RunID, RootRunID: source.RootRunID, StepID: w.step.ID,
			WorkflowName: source.WorkflowName, WorkflowVersion: int64(source.WorkflowVersion), Provider: provider, Model: model}, spent.Totals, incomplete); err != nil {
			log.Printf("recording step %d usage (%s): %v", w.step.ID, inv.Operation, err)
		}
		if incomplete != "" {
			res.UsageIncomplete = meta.Invocation + ": " + incomplete
		}
		if perr := piharness.LastModelError(out); perr != "" && strings.TrimSpace(res.Text) == "" {
			res.ProviderError = perr
			a.progress.Append(w.step.ID, "✗ the model provider refused: "+strs.Truncate(perr, 200))
		}
	}
	// The transcript is kept before anything later runs in the session.
	return res, a.keepWorkerTranscript(ctx, w, meta, res.results, res.resultsErr)
}

// readLog reads an invocation's log, page by page, as far as
// maxInvocationOutput: what it printed on both streams, for a failure's
// words only. A log that cannot be read is empty; nothing is decided by it.
func (a *Executor) readLog(ctx context.Context, operation string, ref execution.TaskRef) []byte {
	r := a.worker.Tasks
	var out []byte
	var cursor int64
	for len(out) < maxInvocationOutput {
		var page execution.OutputPage
		if err := r.whileUnreachable(ctx, operation, func(ctx context.Context) (err error) {
			page, err = r.Driver.Output(ctx, ref, execution.OutputRead{Cursor: cursor, Max: outputPage})
			return err
		}); err != nil || len(page.Data) == 0 || page.Next <= cursor {
			return out
		}
		out = append(out, page.Data...)
		cursor = page.Next
	}
	return out[:maxInvocationOutput]
}

// invocationEvents is an invocation's events, as the worker captured its
// events file when it exited: an error, never an empty stream, when they
// cannot be read.
func invocationEvents(results map[string]execution.ResultFile, readErr error) ([]byte, error) {
	if readErr != nil {
		return nil, fmt.Errorf("reading the invocation's events: %w", readErr)
	}
	f, found := results["events"]
	switch {
	case !found:
		return nil, errors.New("the worker returned no events file")
	case f.State == execution.ResultAbsent:
		return nil, errors.New("the invocation's events file was not there when it exited")
	case f.State == execution.ResultRefused:
		return nil, fmt.Errorf("the worker refused to read the invocation's events: %s", f.Reason)
	case f.State == execution.ResultTooLarge:
		return nil, fmt.Errorf("the invocation's events are %d bytes, over the %d MiB read", f.Size, maxEventsResult>>20)
	case f.State != execution.ResultPresent:
		return nil, fmt.Errorf("the invocation's events file is %s", f.State)
	}
	return f.Content, nil
}

// keepWorkerTranscript keeps an invocation's transcript, captured by the
// worker at the path AppTown named and Pi was pointed at: its header must
// name the admitted session and it must hold more than the header
// (keepCapture). One that cannot be kept is recorded as unavailable, with
// why. An error means neither could be recorded.
func (a *Executor) keepWorkerTranscript(ctx context.Context, w *workerAttempt, meta agentAdmission, results map[string]execution.ResultFile, readErr error) error {
	f, found := results["transcript"]
	reason := ""
	switch {
	case readErr != nil:
		reason = "the worker's result files could not be read: " + readErr.Error()
	case !found:
		reason = "the worker returned no transcript"
	case f.State == execution.ResultAbsent:
		reason = "the transcript was not there when the invocation exited"
	case f.State == execution.ResultRefused:
		reason = "the worker refused to read the transcript: " + f.Reason
	case f.State == execution.ResultTooLarge:
		reason = fmt.Sprintf("the transcript is %d bytes, over the %d MiB kept", f.Size, maxTranscriptResult>>20)
	default:
		reason = piharness.CheckTranscript(f.Content, meta.Session)
	}
	return a.keepCapture(ctx, workflow.TranscriptCapture{Run: workflow.RunRef{ID: w.run.ID, ProjectID: w.run.ProjectID},
		StepID: w.step.ID, Attempt: w.step.OwnedAttempt(), Session: meta.Session, Invocation: meta.Number}, f.Content, reason)
}

// finishAttempt ends the attempt after its last invocation: its commits are
// collected once as the attempt's candidate, its output and written files
// read, the candidate published (or kept refused), and its outcome recorded
// with its artifacts; then its invocations are released, then its
// workspace. A result not kept holds the workspace; a publication not
// decided leaves everything recorded for a restarted AppTown.
func (a *Executor) finishAttempt(ctx context.Context, w *workerAttempt, inv tasks.Attempt, ref execution.TaskRef, res invocationResult, code int, meta agentAdmission) {
	r := a.worker.Tasks
	hub := w.source.Repository(a.cfg.DataDir)
	// A review's verdict and scope are judged from what was recorded and
	// the candidate before anything is published.
	// A review publishes nothing (discardReviewerChanges): its candidate is
	// kept as evidence of what it left, and the branch does not move.
	var review reviewJudgement
	publish := func(c repositoryedit.Candidate) (repositoryedit.Candidate, error) {
		// The step's work exists on the worker: it is published though the
		// caller stops.
		return repositoryedit.PublishTaskResult(context.WithoutCancel(ctx), a.db, a.cfg.DataDir, a.branches, a.publisher, w.run, w.step, w.folder, c, a.now())
	}
	if meta.Review != nil {
		publish = nil
	}
	candidate, published, ok := r.keepAndPublish(w.task, inv, hub, func(c repositoryedit.Candidate) bool {
		if meta.Review != nil {
			review = a.judgeReview(ctx, w, meta, res, hub, c)
		}
		return true
	}, publish)
	if !ok {
		return
	}
	w.obs.harness(res.piResult)
	var settled string
	var err error
	if meta.Review != nil {
		settled, err = a.settleReview(w.task, review.verdict, review.failure, review.evidence(r.backend(), inv.Source, candidate))
	} else {
		settled, err = a.recordWorkerAgent(ctx, w, inv, res, meta, hub, candidate, published)
	}
	w.obs.settle(settled)
	if err != nil && !errors.Is(err, workflow.ErrStepAttemptLost) {
		// The outcome is not recorded: the invocation stays collecting,
		// and a restarted AppTown reads and settles it again.
		log.Printf("task %s: settling its step: %v; it stays recorded for recovery", inv.Operation, err)
		return
	}
	r.settleTask(ctx, inv, ref, &code, "")
	r.releaseAttempt(ctx, inv)
}

// recordWorkerAgent records an agent step's outcome from its candidate and
// result files, with its artifacts.
func (a *Executor) recordWorkerAgent(ctx context.Context, w *workerAttempt, inv tasks.Attempt, res invocationResult, meta agentAdmission, hub string, candidate repositoryedit.Candidate, published error) (string, error) {
	h, filesErr := candidateHarvest(ctx, hub, candidate, res.Text)
	rec := &workerRecord{image: meta.Image, backend: a.worker.Tasks.backend(), source: inv.Source, candidate: candidate, published: published, privateDocker: meta.PrivateDocker, profile: meta.Profile}
	switch {
	case errors.Is(res.outputErr, errTaskGone):
		// Its result is kept; what it said is not.
		rec.problem = "the worker lost this step's work: its invocation and output are gone"
	case filesErr != nil:
		rec.problem = "the step's written files could not be read from its result: " + filesErr.Error()
	}
	if w.cfg.Output.File != "" && rec.problem == "" {
		rec.problem = applyDeclaredOutput(&h, w.cfg.Output.File, res.results, res.resultsErr)
	}
	if res.outputErr != nil {
		rec.outputUnread = res.outputErr.Error()
	}
	var st steeringFile
	st.Hash, st.GuidanceHash, st.Skills, st.Resources = meta.Steering.Hash, meta.Steering.GuidanceHash, meta.Steering.Skills, meta.Steering.Resources
	status, err := a.recordAgentOutcome(ctx, agentRecord{task: w.task, cfg: &w.cfg, pi: res.piResult, files: h, steering: st,
		modelSlot: meta.ModelSlot, modelProfile: meta.ModelProfile, executor: execution.Docker, image: meta.Image.Name,
		start: meta.Began, queuedMs: meta.QueuedMs, attempts: meta.Dispatches, worker: rec})
	return settlementOutcome(status, err), err
}

// releaseAttempt releases each settled invocation of inv's attempt not
// released yet, then the attempt's workspace, once every invocation in it
// is released. What is not confirmed is tried again (ReleaseSettled).
func (r *TaskRunner) releaseAttempt(ctx context.Context, inv tasks.Attempt) {
	ctx, cancel := briefly(ctx)
	invocations, err := r.Records.Invocations(ctx, inv.Owner.StepID, inv.Owner.Attempt)
	cancel()
	if err != nil {
		log.Printf("task %s: reading its attempt's invocations to release them: %v; they are released later", inv.Operation, err)
		return
	}
	for _, other := range invocations {
		if other.Observed == tasks.Settled && !other.Released {
			r.releaseOwn(ctx, other)
		}
	}
	r.releaseWorkspace(ctx, inv.Workspace)
}

// workerRecord is what an attempt on the worker adds to its outcome: where
// it ran, its result as kept, and whatever made its declared results
// unreadable.
type workerRecord struct {
	image     execution.AgentImage
	backend   string
	source    string
	candidate repositoryedit.Candidate
	published error
	// problem makes the step an error: its declared output file or its
	// written files could not be read.
	problem string
	// outputUnread: the invocation's output could not be read; its usage
	// is unknown.
	outputUnread string
	// privateDocker is what its private Docker rested on, and profile the
	// worker's profile (agentAdmission).
	privateDocker string
	profile       *execution.WorkerProfile
}

// declaredOutput reads the step's declared output file, as the worker
// captured it. Absent is "" with no problem; refused or too large is a
// problem, never "no output".
// applyDeclaredOutput makes the step's declared output file, as the worker
// returned it, the harvest's output (useDeclared); it returns why the file
// could not be read, "" when it could.
func applyDeclaredOutput(h *workspaceHarvest, file string, results map[string]execution.ResultFile, readErr error) string {
	body, problem := declaredOutput(results, readErr)
	if problem != "" {
		return "the output file could not be read: " + problem
	}
	h.useDeclared(file, body)
	return ""
}

func declaredOutput(results map[string]execution.ResultFile, readErr error) (string, string) {
	if readErr != nil {
		return "", readErr.Error()
	}
	f, found := results["output"]
	switch {
	case !found:
		return "", "the worker returned no result for it"
	case f.State == execution.ResultPresent:
		return string(f.Content), ""
	case f.State == execution.ResultAbsent:
		return "", ""
	case f.State == execution.ResultRefused:
		return "", "the worker refused to read it (" + f.Reason + ")"
	case f.State == execution.ResultTooLarge:
		return "", fmt.Sprintf("it is %d bytes, over the %d MiB an output may be", f.Size, maxOutputResult>>20)
	}
	return "", fmt.Sprintf("its result is %q", f.State)
}

// Written files kept from a step's result, as AppTown's own harvest keeps
// them.
const (
	maxWrittenFile  = 256 << 10
	maxWrittenFiles = 40
)

// candidateHarvest is what the attempt wrote, read from its candidate in the
// hub (hardened, blobs only): the files it added or changed from its source
// to its tip, text only, each at most 256 KiB, at most 40; and the output
// body, the single markdown file among them or else text. Deleted files are
// not written files, and neither are links or submodules.
func candidateHarvest(ctx context.Context, hub string, c repositoryedit.Candidate, text string) (workspaceHarvest, error) {
	h := workspaceHarvest{Output: text}
	if !c.Changed() {
		return h, nil
	}
	if !commitPattern.MatchString(c.Source) || !commitPattern.MatchString(c.Tip) {
		return h, fmt.Errorf("the result's commits %q..%q are not commits", c.Source, c.Tip)
	}
	raw, err := gitexec.HubOutput(ctx, hub, nil, "diff-tree", "-r", "-z", "--no-renames", "--diff-filter=AM", c.Source, c.Tip)
	if err != nil {
		return h, fmt.Errorf("listing the changed files: %w", err)
	}
	fields := bytes.Split(bytes.TrimSuffix(raw, []byte{0}), []byte{0})
	if len(raw) == 0 {
		fields = nil
	}
	if len(fields)%2 != 0 {
		return h, errors.New("the changed files' listing is malformed")
	}
	for i := 0; i < len(fields) && len(h.Files) < maxWrittenFiles; i += 2 {
		meta := strings.Fields(strings.TrimPrefix(string(fields[i]), ":"))
		name := string(fields[i+1])
		if len(meta) != 5 {
			return h, fmt.Errorf("the changed files' listing is malformed at %q", fields[i])
		}
		if mode := meta[1]; mode != "100644" && mode != "100755" {
			continue
		}
		size, err := gitexec.HubOutput(ctx, hub, nil, "cat-file", "-s", meta[3])
		if err != nil {
			return h, fmt.Errorf("reading %s: %w", name, err)
		}
		if n, err := strconv.ParseInt(strings.TrimSpace(string(size)), 10, 64); err != nil || n > maxWrittenFile {
			continue
		}
		body, err := gitexec.HubOutput(ctx, hub, nil, "cat-file", "blob", meta[3])
		if err != nil {
			return h, fmt.Errorf("reading %s: %w", name, err)
		}
		if bytes.IndexByte(body, 0) >= 0 {
			continue // binary
		}
		h.Files = append(h.Files, wroteFile{rel: name, body: string(body)})
	}
	var markdown []wroteFile
	for _, f := range h.Files {
		if strings.HasSuffix(f.rel, ".md") {
			markdown = append(markdown, f)
		}
	}
	if len(markdown) == 1 {
		h.Output, h.MdFile = markdown[0].body, markdown[0].rel
	}
	return h, nil
}
