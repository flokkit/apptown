package workflowexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"flokkit.io/forge/internal/ids"

	piharness "flokkit.io/forge/internal/forge/agentharness/pi"
	"flokkit.io/forge/internal/gitexec"
	"flokkit.io/forge/internal/poll"
	"flokkit.io/forge/internal/strs"

	"flokkit.io/forge/internal/execution"

	models "flokkit.io/forge/internal/forge/model"
	"flokkit.io/forge/internal/forge/repositoryedit"
	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/forge/workflow"
)

// agent/review — the cop model. An INDEPENDENT agent verifies a producing
// step's work before the human sees it: inputs are the request (the change),
// the actual diff (authoritative), and the implementer's report (a claim to
// be checked, not believed). Output is a structured verdict that gates like
// any check. This exists because self-reports are unreliable — an
// implementer that silently delivers Y ⊂ X passes report-based review;
// claim-vs-diff verification is a different job and gets a different agent
// with fresh context.
//
// A fail feeds ${{feedback.latest}} exactly like a human rejection, so
// re-running the producing step delivers the reviewer's findings to the
// implementer. Verdicts never merge anything: the review is one more gate;
// humans stay the judgment layer above it.

const reviewTimeout = 10 * time.Minute
const maxReviewDiff = 64 * 1024

type reviewVerdict struct {
	Verdict  string   `json:"verdict"` // pass | fail
	Summary  string   `json:"summary"`
	Findings []string `json:"findings"`
}

// reviewConfig is an agent/review step's configuration.
type reviewConfig struct {
	Of     string `json:"of"`     // report artifact kind to audit (optional)
	Prompt string `json:"prompt"` // extra reviewer focus (optional)
	// Question replaces the shared first question, "does the diff fully
	// satisfy the request?", for a review whose step is not asked to
	// satisfy it: a reproduction is asked to show the bug, not to fix it,
	// and judged by the shared question, a reproducer that could not
	// reproduce is pushed to fake one and apply the fix.
	Question       string `json:"question"`
	Model          string `json:"model"`
	Thinking       string `json:"thinking"`
	TimeoutMinutes int    `json:"timeoutMinutes"`
}

// Review executes an agent/review step: on the forge's worker when it has
// one (runOnWorker), never on its own machine instead.
func (a *Executor) Review(task *workflow.Execution) {
	defer reportPanic(a.panics, "RunAgentReview")
	step := task.Step()
	stepID := step.ID
	run := task.Run()
	var cfg reviewConfig
	cfgErr := workflow.DecodeStepConfig(step.Config, &cfg)

	attempt := a.obs.begin(task.Context(), run.ID, step.ID, "agent/review", run.WorkflowName, run.ProjectID)
	defer attempt.settle("abandoned")
	finish := func(status workflow.StepState, evidence map[string]any) {
		attempt.settle(settlementOutcome(status, task.Finish(workflow.EncodeOutcome(status, evidence, nil))))
	}
	if cfgErr != nil {
		if _, claimErr := task.Claim(); claimErr == nil {
			finish(workflow.StatusError, map[string]any{"reason": cfgErr.Error()})
		}
		return
	}

	// A review of a tree that did not boot is not a review: the boot in
	// this stage settles first (a median 5 s), and if it failed the
	// reviewer's minutes are not spent — the verdict is the boot's reason,
	// and the rework the boot already asked for carries it. Otherwise the
	// reviewer spends minutes on the same tree only to fail it again.
	if boot := a.awaitStageBoot(task.Context(), run.ID, step.Stage); boot != nil && (boot.Status == workflow.StatusFail || boot.Status == workflow.StatusError) {
		if claim, err := task.Claim(); err != nil {
			return
		} else {
			*step = *claim
		}
		reason := "the app did not boot"
		var ev struct {
			Reason  string `json:"reason"`
			LogTail string `json:"logTail"`
		}
		if json.Unmarshal(boot.Evidence, &ev) == nil && ev.Reason != "" {
			reason += ": " + ev.Reason
		}
		// This verdict settles after the boot's, so it is the newest
		// failure ${{feedback.latest}} reads: it carries the boot's log
		// tail, or the agent is told only that the app did not come up and
		// never why (a missing table, a crash on start).
		verdict := map[string]any{"verdict": "fail", "summary": reason + " — nothing to review until it does",
			"findings": []string{reason}, "notRun": "the reviewer was not consulted: the boot step already failed this tree"}
		if ev.LogTail != "" {
			verdict["logTail"] = ev.LogTail
		}
		attempt.settle(reworkOutcome(task.Finish(workflow.EncodeRework(verdict))))
		// The boot's reason goes back to the agent as a review's would:
		// a tree that does not build or come up is the agent's to fix,
		// and without a send-back the change waits with nobody asked to
		// fix it.
		return
	}

	// The wait is the backend's: the review draws a slot from the pool of
	// the binding its model runs on (model_slots.go), so a reviewer on a
	// hosted API never queues behind an implementer on the GPU box.
	model, modelSlot, modelProfile := a.modelSelection.Resolve(task.Context(), run.ProjectID, cfg.Model)
	binding := models.BindingOfPattern(model)
	queuedAt := a.now()
	releaseSlot, waited := a.capacity.Acquire(task.Context(), binding, step.ID)
	defer releaseSlot()
	if waited {
		recordEvent(task.Context(), a.db, step.RunID, "step.queued", map[string]any{"step": step.Name, "reason": "waited for a slot on " + binding, "ms": a.now().Sub(queuedAt).Milliseconds()}, a.now())
	}
	queuedMs := a.now().Sub(queuedAt).Milliseconds()
	attempt.queued(queuedAt, binding)
	if a.worker != nil {
		a.runOnWorker(task, attempt, &reviewCall{cfg: cfg}, queuedMs, finish)
		return
	}

	// The folder is waited for before the claim (see Run).
	source, err := workflow.ReadSourceIn(task.Context(), a.db, workflow.RunRef{ID: run.ID, ProjectID: run.ProjectID})
	if err != nil {
		// Unclaimed, the step stays pending for the dispatcher to retry.
		log.Printf("run %d step %d: reading its workspace: %v; not started", run.ID, step.ID, err)
		attempt.settle("unclaimed")
		return
	}
	ws := source.Workspace(a.cfg.DataDir)
	folder := gitexec.LockFolder(ws)
	defer folder.Release()
	if claim, err := task.Claim(); err != nil {
		attempt.settle("unclaimed")
		return
	} else {
		*step = *claim
		attempt.claimed(claim.Attempt)
	}
	if err := earlierTasksStopped(task.Context(), a.db, run, step); err != nil {
		finish(workflow.StatusError, map[string]any{"reason": err.Error()})
		return
	}
	if a.docker != nil {
		if err := a.folderReady(task.Context(), step.ID, ws); err != nil {
			finish(workflow.StatusError, map[string]any{"reason": err.Error()})
			return
		}
	}
	// The branch in the hub is what the change has published; its base
	// there is an ancestor of the folder's HEAD too.
	base, err := reviewBase(task.Context(), source.Repository(a.cfg.DataDir), source, "refs/heads/"+source.Branch)
	if err != nil && source.RepoBacked() {
		finish(workflow.StatusError, map[string]any{"reason": "reading the change's base for the reviewer: " + err.Error()})
		return
	}
	scope := func(ctx context.Context) (string, []repositoryedit.ChangedFile, error) {
		files, err := a.changedFiles(ctx, ws, base)
		return base, files, err
	}
	brief, err := a.reviewBrief(task.Context(), run, source, cfg, reviewDiff(task.Context(), a.cfg.DataDir, source, base), scope)
	if err != nil {
		finish(workflow.StatusError, map[string]any{"reason": err.Error()})
		return
	}
	_ = os.MkdirAll(filepath.Join(ws, ".forge"), 0o755)
	// What the reviewer leaves in the folder is discarded afterwards
	// (discardReviewerChanges): the folder goes back to this commit.
	var reviewed string
	if source.RepoBacked() {
		if reviewed, err = gitexec.Run(task.Context(), ws, "rev-parse", "--verify", "HEAD^{commit}"); err != nil {
			finish(workflow.StatusError, map[string]any{"reason": "reading the commit under review: " + err.Error()})
			return
		}
		reviewed = strings.TrimSpace(reviewed)
	}
	// The prompt travels as ONE argv element; Linux caps a single arg at
	// ~128KB (MAX_ARG_STRLEN), and a large excavated report blew it — the
	// review container died at exec with "argument list too long" (run
	// 102). Large claims are staged as a workspace file instead; the
	// reviewer works in the same worktree and reads it from disk.
	if staged, pointer := stagedClaim(brief.claim, ".forge/review-claim.md"); staged != nil {
		if err := os.WriteFile(filepath.Join(ws, ".forge", "review-claim.md"), staged, 0o644); err == nil {
			brief.claim = pointer
		}
	}

	verdictPath := filepath.Join(ws, ".forge", "review.json")
	_ = os.Remove(verdictPath) // never trust a stale verdict

	thinking := cfg.Thinking
	if thinking == "" {
		thinking = "medium"
	}

	env, err := a.stepEnv(task.Context(), run.ProjectID)
	if err != nil {
		finish(workflow.StatusError, map[string]any{"reason": err.Error()})
		return
	}
	// The reviewer verifies claims like "apptown check passed" itself: the
	// real CLI and read-only credentials, exactly as implementation steps
	// get them. A reviewer with neither may fetch a PyPI package named
	// forge and fail true claims against it.
	env = append(env, a.forgeEnv(task.Context(), run, step)...)
	executor := a.cfg.Backend
	if executor == execution.Docker {
		if _, err := a.home.Stage(task.Context(), ws, run.ProjectID); err != nil {
			finish(workflow.StatusError, map[string]any{"reason": err.Error()})
			return
		}
		env = append(env, "HOME=/work/.forge/pi-home")
	}
	timeout := reviewTimeout
	if cfg.TimeoutMinutes > 0 {
		timeout = time.Duration(cfg.TimeoutMinutes) * time.Minute
	}

	start := a.now()
	attempt.run()
	a.progress.Start(stepID)
	defer workflow.CaptureProgress(task.Context(), a.db, a.progress, stepID, a.clock)
	// One invocation shape for the initial run and both wrap-up resumes:
	// the same env and the same mounts (CLI included) every time. The
	// resume/fixup copies used to pass different mounts, so a resumed
	// review lost the CLI it was given to verify claims.
	pi := func(args []string, timeout time.Duration, name string) piResult {
		return a.piInvoke(task.Context(), piCall{
			Source: source, Step: step, Executor: executor, Image: a.cfg.Image, Workspace: ws,
			Args: args, Env: env, Timeout: timeout, Name: name,
		})
	}
	outcome := pi(piharness.ReviewArgs(model, thinking, false, brief.prompt()), timeout,
		execution.UniqueName(fmt.Sprintf("forge-review-%d", stepID)))
	attempt.harness(outcome)

	evidence := map[string]any{
		"model": outcome.Model, "provider": outcome.Provider, "modelSlot": modelSlot, "modelProfile": modelProfile,
		"piVersion":  a.docker.AgentRuntimeVersion(task.Context(), a.cfg.Image),
		"durationMs": a.now().Sub(start).Milliseconds(), "queuedMs": queuedMs,
		"reviewedReport": cfg.Of,
	}
	v, failure := a.readReviewVerdict(stepID, verdictPath, model, outcome.TimedOut, pi, evidence)
	// The reviewer has run: what it left is judged and settled though the
	// caller stops, so a stop is never read as an unassessed scope.
	settling := context.WithoutCancel(task.Context())
	if failure == "" {
		if err := a.guardReviewScope(settling, run, scope, cfg.Of, brief.claim, &v, evidence); err != nil {
			// A verdict over a scope nobody could read is not a review: the
			// guard's refusals are deterministic, and an unread diff would
			// pass them all.
			failure = scopeUnassessed(err)
		}
	}
	if reviewed != "" {
		if err := discardReviewerChanges(settling, folder, reviewed); err != nil && failure == "" {
			failure = err.Error()
		}
	}
	// From the invocation on, the reviewer's minutes are spent: an outcome
	// whose recording fails is recorded again, not reviewed again
	// (settleProduced).
	settled, _ := a.settleReview(task, v, failure, evidence)
	attempt.settle(settled)
}

// discardReviewerChanges puts the run's folder back at the commit the
// reviewer was given, keeping .forge (the verdict, the sessions, the
// collected reports). A review commits nothing: reviewers judge the work,
// they do not change it (design/architecture/workflow-spec.md, "agent/review").
// Whatever a reviewer leaves behind (a __pycache__ from running the tests, a
// scratch edit) is not the change's: committed as the review's step, the next
// review would fail the implementer for it, and left in the folder, the next
// producing step would commit it under its own name. An error means the folder may still hold them.
func discardReviewerChanges(ctx context.Context, folder *gitexec.Folder, reviewed string) error {
	if err := folder.Reset(ctx, reviewed, ".forge"); err != nil {
		return fmt.Errorf("discarding what the reviewer left in the folder: %w", err)
	}
	return nil
}

// settleReview records a review's outcome: an error with its reason when no
// verdict counts (failure); a pass; or a fail, as a machine rework. It
// returns the observer's settlement and the error when the outcome could
// not be recorded.
func (a *Executor) settleReview(task *workflow.Execution, v reviewVerdict, failure string, evidence map[string]any) (string, error) {
	status := workflow.StatusError
	switch {
	case failure != "":
		evidence["reason"] = failure
	default:
		evidence["verdict"], evidence["summary"], evidence["findings"] = v.Verdict, v.Summary, v.Findings
		switch {
		case v.Verdict == "pass":
			status = workflow.StatusPass
		default:
			evidence["reason"] = "reviewer rejected: " + v.Summary
			// The forge's file check is not the reviewer: when it failed a
			// review the model passed, the failure is the forge's, and the
			// reviewer's own summary is kept apart as the pass it was.
			if check, _ := evidence["fileCheck"].(string); check != "" {
				if evidence["reviewerVerdict"] == "pass" {
					evidence["reason"] = "AppTown's file check failed: " + check
					evidence["summary"], evidence["reviewerSummary"] = evidence["reason"], v.Summary
				} else {
					evidence["reason"] = "reviewer rejected: " + v.Summary + "; AppTown's file check also failed: " + check
				}
			}
			// A machine verdict earns a machine rework: the findings are
			// already in ${{feedback.latest}}; no human should be the
			// transport for them.
			recorded, err := a.settleProducedStep(task, workflow.StatusFail, func() error { return task.Finish(workflow.EncodeRework(evidence)) })
			if recorded == workflow.StatusFail {
				return reworkOutcome(err), err
			}
			return settlementOutcome(recorded, err), err
		}
	}
	recorded, err := a.settleProducedStep(task, status, func() error { return task.Finish(workflow.EncodeOutcome(status, evidence, nil)) })
	return settlementOutcome(recorded, err), err
}

// reviewBrief is everything the reviewer is shown: the request, the
// implementer's claim, the authoritative diff, the notes the forge adds, and
// any extra focus the workflow asks for.
type reviewBrief struct {
	request, claim, diff, notes, extra string
	// question is the step's own first question (reviewConfig.Question);
	// empty is the shared one.
	question string
}

// The shared first question and the verdict rules that follow from it.
const (
	sharedQuestion = `Does the diff fully satisfy the request — every part of it? A partial
   implementation that quietly skips part of the request is a FAIL. When the
   base already does what the request asks, an empty diff satisfies it: judge
   that from the base itself (read it, run "apptown check"), and do not fail a
   change for not adding what nothing needed.`
	sharedUnmet      = "the request not fully satisfied"
	sharedMet        = "the code fully satisfies the request"
	sharedFailRule   = "the request is not fully satisfied"
	ownQuestionUnmet = "the answer to question 1 is no"
	ownQuestionMet   = "the answer to question 1 is yes"
)

func (b reviewBrief) prompt() string {
	question, unmet, met, failRule := sharedQuestion, sharedUnmet, sharedMet, sharedFailRule
	if q := strings.TrimSpace(b.question); q != "" {
		question, unmet, met, failRule = q, ownQuestionUnmet, ownQuestionMet, ownQuestionUnmet
	}
	return fmt.Sprintf(reviewPromptTemplate, b.request, b.claim, b.diff, b.notes, b.extra, question, unmet, met, failRule)
}

// scopeReader lists the files a change touched against its base
// (reviewBase), and that base: from the run's folder on the forge's own
// machine, from the hub for a result on a worker. An error is "could not
// assess", never an empty scope.
type scopeReader func(ctx context.Context) (base string, files []repositoryedit.ChangedFile, err error)

// reviewBrief assembles the reviewer's brief from the run's records, the
// change's diff and its scope.
func (a *Executor) reviewBrief(ctx context.Context, run *workflow.Run, source workflow.Source, cfg reviewConfig, diff string, scope scopeReader) (reviewBrief, error) {
	var item *workflow.Change
	if !run.ChangeID.IsZero() {
		item, _ = workflow.ReadChangeIn(ctx, a.db, run.ChangeID)
	}
	slug := source.Slug
	b := reviewBrief{request: "(no change)", diff: diff}
	if item != nil {
		b.request = item.Title + "\n\n" + item.Body
	}
	// A person's send-back note is part of the request: without it a
	// reviewer can call the upgrade a person asked for "unrelated scope".
	asked, err := workflow.LatestPersonFeedbackIn(ctx, a.db, workflow.RunRef{ID: run.ID, ProjectID: run.ProjectID})
	if err != nil {
		return reviewBrief{}, err
	}
	if asked != nil {
		b.request += "\n\nA PERSON SENT THIS WORK BACK, ASKING (part of the request; work it asks for is in scope):\n" + asked.Note + " (— " + asked.By + ")"
	}

	// The implementer's claim (a report artifact on this run), if configured.
	b.claim = "(no report artifact configured for this review)"
	if cfg.Of != "" {
		reports, err := workflow.ReadRunArtifactsIn(ctx, a.db, workflow.RunRef{ID: source.RootRunID, ProjectID: run.ProjectID}, workflow.ArtifactSelection{Kinds: []string{cfg.Of}, Latest: true})
		if err != nil {
			return reviewBrief{}, err
		}
		if len(reports) > 0 {
			b.claim = reports[0].Body
		} else {
			b.claim = fmt.Sprintf("(no %q artifact found — the implementer produced no report; that itself is reviewable)", cfg.Of)
		}
	}

	steps, err := workflow.ReadScopedStepsIn(ctx, a.db, workflow.RunRef{ID: run.ID, ProjectID: run.ProjectID})
	if err != nil {
		return reviewBrief{}, err
	}
	note, err := a.scopeNote(ctx, run, scope)
	if err != nil {
		return reviewBrief{}, errors.New(scopeUnassessed(err))
	}
	b.notes = mandatedOutputsNote(steps) + note + undecidedFindingsNote(steps)

	if cfg.Prompt != "" {
		b.extra = "\nADDITIONAL REVIEW FOCUS:\n" + workflow.Interpolate(ctx, a.db, cfg.Prompt, run, item, slug) + "\n"
	}
	if cfg.Question != "" {
		b.question = workflow.Interpolate(ctx, a.db, cfg.Question, run, item, slug)
	}

	return b, nil
}

// largeClaim is the size above which a review's claim is staged as a file
// instead of travelling in the prompt.
const largeClaim = 48 << 10

// stagedClaim: a claim too large for the prompt is staged as a file at
// path, and the brief carries pointer instead. staged is nil for a claim
// the prompt can carry.
func stagedClaim(claim, path string) (staged []byte, pointer string) {
	if len(claim) <= largeClaim {
		return nil, claim
	}
	return []byte(claim), "(the report is large; it is staged at " + path + " — READ THAT FILE FIRST, it is the claim under review)"
}

// reviewBase is where the reviewer's diff starts, and it is the base the
// scope rules read the change's files from (guardReviewScope): the change's
// base when the run is on a change, else where the run started. One base for
// both, because the report must describe the WHOLE change (the lanes' prompts
// say so) and the unclaimed-file rule holds it to that. A diff from where the
// run started, beside a scope from the change's base, leaves no honest report
// that satisfies both after a send-back: the reviewer calls the earlier
// rounds' files "claimed but absent", the scope rule calls them unclaimed if
// left out, and reviews loop until a human overrides them. The change's base
// is scm.ChangeBase, read in the hub against tip: after the change catches up
// with main it is the main it merged, so main's files are not held against
// the report. Off a change it is where the run started, else the default
// branch; without a repository, only what was recorded.
func reviewBase(ctx context.Context, hub string, source workflow.Source, tip string) (string, error) {
	recorded := strs.FirstNonBlank(source.ChangeBaseSHA, source.RunBaseSHA)
	switch {
	case !source.RepoBacked():
		return recorded, nil
	case !source.ChangeID.IsZero():
		return scm.ChangeBase(ctx, hub, source.DefaultBranch, tip, recorded)
	case recorded != "":
		return recorded, nil
	}
	return "refs/heads/" + source.DefaultBranch, nil
}

// reviewDiff is the authoritative diff, from reviewBase to the branch now.
func reviewDiff(ctx context.Context, dataDir string, source workflow.Source, base string) string {
	if !source.RepoBacked() {
		return "(project is not repo-backed — inspect the workspace directly)"
	}
	repo, branch := source.Repository(dataDir), source.Branch
	d, err := gitexec.Run(ctx, repo, "diff", base+"..."+branch)
	if err != nil {
		return "(project is not repo-backed — inspect the workspace directly)"
	}
	return boundedDiff(d)
}

// boundedDiff is a diff as the brief carries it: at most maxReviewDiff, and
// an empty one said in words.
func boundedDiff(d string) string {
	if len(d) > maxReviewDiff {
		d = d[:maxReviewDiff] + "\n... (diff truncated — inspect the workspace for the rest)"
	}
	if strings.TrimSpace(d) == "" {
		d = "(EMPTY DIFF — the branch changes nothing)"
	}
	return d
}

// mandatedOutputsNote names the report files the producing steps are
// REQUIRED to write (e.g. .forge/CHANGE.md), under the publication rule
// (scm.RuntimeOnly): a report under .forge is forge state the forge collects
// from the workspace, never committed (publication refuses it), so it is
// rightly absent from the diff; a report elsewhere is committed source.
// Without the note the reviewer either flags a committed report as scope
// creep, or fails every round demanding a .forge report in the diff.
func mandatedOutputsNote(steps []*workflow.Step) string {
	var collected, committed []string
	for _, step := range steps {
		if step.Type != "agent/pi" {
			continue
		}
		var pc struct {
			Output struct {
				File string `json:"file"`
			} `json:"output"`
		}
		if json.Unmarshal(step.Config, &pc) != nil || pc.Output.File == "" {
			continue
		}
		if scm.RuntimeOnly(pc.Output.File) {
			collected = append(collected, pc.Output.File)
		} else {
			committed = append(committed, pc.Output.File)
		}
	}
	note := ""
	if len(collected) > 0 {
		note += "\nWORKFLOW-MANDATED REPORTS: the workflow requires the implementer to write\n" + strings.Join(collected, ", ") + " and NOT commit it: AppTown collects it from the\nworkspace, and nothing under .forge but .forge/app.yaml may be committed.\nIt is correctly ABSENT from the diff — never a finding, never ask for it to be\ncommitted. The report under review is shown above as THE IMPLEMENTER'S CLAIM.\n"
	}
	if len(committed) > 0 {
		note += "\nWORKFLOW-MANDATED OUTPUTS: the workflow itself requires the implementer to\nwrite these report files: " + strings.Join(committed, ", ") + ". Changes to them are REQUIRED, not scope\ncreep — do not count them against the request.\n"
	}
	return note
}

// scopeNote (scope.go): files the repository ignores that the implementer
// committed anyway are named to the reviewer on every change; on a
// remediation, dependency files it moved are too. The gate refuses both; this
// makes the verdict say why in words. An error means the scope could not be
// read; the brief is then not written at all.
func (a *Executor) scopeNote(ctx context.Context, run *workflow.Run, scope scopeReader) (string, error) {
	if run.ChangeID.IsZero() {
		return "", nil
	}
	ch, err := workflow.ReadChangeIn(ctx, a.db, run.ChangeID)
	if err != nil {
		return "", fmt.Errorf("reading change %d: %w", run.ChangeID, err)
	}
	if ch == nil {
		return "", nil
	}
	note := ""
	_, changed, err := scope(ctx)
	if err != nil {
		return "", err
	}
	if files := repositoryedit.IgnoredChanges(changed); len(files) > 0 {
		note += "\nIGNORED FILES COMMITTED: this change committed " + strings.Join(files, ", ") + ", which the\nrepository's own .gitignore says is not source (build output, or forge runtime\nfiles under .forge/). The verdict must be \"fail\" and the findings must name those\nfiles so the implementer removes them from git (git rm --cached).\n"
	}
	if files := repositoryedit.DependencyChangesOutOfScope(changed, workflow.AcceptanceScopeOf(ch)); len(files) > 0 {
		note += "\nOUT-OF-SCOPE DEPENDENCY CHANGES: this change exists to close one conformance\ncheck, and it changed " + strings.Join(files, ", ") + " beyond adding a package. Moving\ndependencies is not its job, however small or well-meant the bump; the verdict\nmust be \"fail\" and the findings must name those files so the implementer\nreverts them.\n"
	}
	return note, nil
}

// scopeUnassessed is the step's reason when the files a change touched could
// not be read: the scope rules were not applied, which is neither an empty
// scope nor a pass.
func scopeUnassessed(err error) string {
	return "the change's scope could not be assessed: " + err.Error()
}

// undecidedFindingsNote carries findings the conformance gate could not decide
// (design/subsystems/guidance-checks.md): they fail on main and on this
// change and the check reports no occurrences, or main could not be judged
// for them (its deployment runs another commit), so only a reader of the
// diff can say whether the change made them worse. That reader is this
// reviewer.
func undecidedFindingsNote(steps []*workflow.Step) string {
	for i := len(steps) - 1; i >= 0; i-- {
		step := steps[i]
		if step.Type != "guidance/conformance" {
			continue
		}
		ev, err := workflow.ReadCheckEvidence(step.Evidence)
		if err != nil {
			return "\nCONFORMANCE EVIDENCE UNREADABLE: the conformance gate's evidence could not be\nread, so it is not known whether it left findings it could not attribute to this\nchange. Judge the diff on its own.\n"
		}
		if len(ev.Undecided) > 0 {
			return "\nUNDECIDED CONFORMANCE FINDINGS: the gate could not tell whether this change\nintroduced these: either they fail on main too and report no occurrences, or\nmain could not be judged for them (each says why). Judge from the diff: fail the review only if this change ADDED to\nthe problem (a second offending dependency, another tracked file). Do not fail\nit for the pre-existing debt itself — that is the fleet review's, and the\nimplementer was told not to widen the change for it:\n- " + strings.Join(ev.Undecided, "\n- ") + "\n"
		}
		return ""
	}
	return ""
}

// readReviewVerdict reads the reviewer's verdict file, resuming the session
// once when it is missing or invalid. It returns a failure reason when there
// is still no usable verdict.
//
// Timeout is a PAUSE, not a kill: the session transcript
// survives in .forge/sessions, so instead of discarding 20+ minutes of paid
// work we resume it ONCE with a wrap-up order and a small grace budget. The
// verdict file is a paragraph — a resumed session writes it in one turn from
// work already done. The same net catches a clean exit that forgot the file:
// the reviewer did the work, just never wrote the verdict.
func (a *Executor) readReviewVerdict(stepID ids.ID, verdictPath, model string, timedOut bool, pi func([]string, time.Duration, string) piResult, evidence map[string]any) (reviewVerdict, string) {
	if _, statErr := os.Stat(verdictPath); statErr != nil {
		a.progress.Append(stepID, "⏸ no verdict yet — resuming session to write it")
		pi(piharness.ReviewArgs(model, "", true, reviewWrapUp), followUps[reviewWrapUpInvocation].allowance, fmt.Sprintf("forge-review-%d-resume", stepID))
		evidence["resumed"] = true
	}
	raw, err := os.ReadFile(verdictPath)
	if err != nil {
		if timedOut {
			return reviewVerdict{}, "review timed out (no verdict even after wrap-up resume)"
		}
		return reviewVerdict{}, "reviewer produced no .forge/review.json verdict"
	}
	v, ok := parseReviewVerdict(raw)
	if !ok && evidence["resumed"] == nil {
		// Exists-but-invalid is the third face of the same failure (a
		// review.json truncated mid-write). One resume, same wrap-up.
		a.progress.Append(stepID, "⏸ verdict file invalid — resuming session to rewrite it")
		pi(piharness.ReviewArgs(model, "", true, reviewFixUp), followUps[reviewFixUpInvocation].allowance, fmt.Sprintf("forge-review-%d-fixup", stepID))
		evidence["resumed"] = true
		if raw2, err2 := os.ReadFile(verdictPath); err2 == nil {
			if v2, ok2 := parseReviewVerdict(raw2); ok2 {
				raw, v, ok = raw2, v2, true
			}
		}
	}
	if !ok {
		evidence["raw"] = strs.Truncate(string(raw), 1000)
		return reviewVerdict{}, "reviewer verdict unparseable"
	}
	return v, ""
}

// guardReviewScope applies the scope rules deterministically (scope.go): on a
// remediation, dependency files and build output in the diff fail the review
// whatever the model concluded, naming the files — a model can read a
// rewritten target/ as "only README.md changed".
// An error means the changed files could not be read and no rule was applied;
// the caller must not record the verdict.
func (a *Executor) guardReviewScope(ctx context.Context, run *workflow.Run, scope scopeReader, reportKind, claim string, v *reviewVerdict, evidence map[string]any) error {
	if run.ChangeID.IsZero() {
		return nil
	}
	ch, err := workflow.ReadChangeIn(ctx, a.db, run.ChangeID)
	if err != nil {
		return fmt.Errorf("reading change %d: %w", run.ChangeID, err)
	}
	if ch == nil {
		return nil
	}
	base, changed, err := scope(ctx)
	if err != nil {
		return err
	}
	// Always on the receipt, so a guard that saw nothing is distinguishable
	// from one that never ran.
	names := make([]string, 0, len(changed))
	for _, c := range changed {
		names = append(names, c.Path)
	}
	evidence["scopeSaw"] = map[string]any{"base": strs.Truncate(base, 12), "files": names}
	// failed names each rule the forge's own file check failed, kept apart
	// from the reviewer's verdict: a model's pass failed by the forge is not
	// the reviewer rejecting the work (settleReview).
	var guard, failed []string
	reviewerVerdict := v.Verdict
	defer func() {
		if len(failed) > 0 {
			evidence["fileCheck"], evidence["reviewerVerdict"] = strings.Join(failed, "; "), reviewerVerdict
		}
	}()
	if files := repositoryedit.IgnoredChanges(changed); len(files) > 0 {
		failed = append(failed, countFiles(len(files), "ignored file")+" committed")
		v.Verdict = "fail"
		v.Findings = append([]string{"ignored files committed: " + strings.Join(files, ", ") + " — the repository's own .gitignore says these are not source (build output, or forge runtime files under .forge/); remove them from git (git rm --cached <path>) and commit only the fix"}, v.Findings...)
		guard = append(guard, files...)
	}
	if files := repositoryedit.DependencyChangesOutOfScope(changed, workflow.AcceptanceScopeOf(ch)); len(files) > 0 {
		failed = append(failed, countFiles(len(files), "dependency file")+" changed out of scope")
		v.Verdict = "fail"
		v.Findings = append([]string{"out of scope for a remediation: " + strings.Join(files, ", ") + " — dependencies (beyond adding a package the fix needs) are not this change's to move; restore them exactly as they were on the base (git checkout <base> -- <path>) and commit only the fix"}, v.Findings...)
		guard = append(guard, files...)
	}
	if len(guard) > 0 {
		evidence["scopeGuard"] = guard
	}
	// Every changed file must be named in the report (scope.go): an unclaimed
	// change reads as deception, and a model missed a rewritten REPORT.md the
	// report never mentioned (247).
	if reportKind != "" {
		if unclaimed := repositoryedit.UnclaimedChanges(changed, claim); len(unclaimed) > 0 {
			failed = append(failed, countFiles(len(unclaimed), "changed file")+" the report doesn't mention")
			v.Verdict = "fail"
			v.Findings = append([]string{"unclaimed change: " + strings.Join(unclaimed, ", ") + " — the report names none of these; every changed file must be accounted for: restore it to the base if it was not needed, or say what changed and why"}, v.Findings...)
			evidence["unclaimed"] = unclaimed
		}
	}
	return nil
}

func countFiles(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// The resume orders for a reviewer that left no usable verdict.
const reviewWrapUp = `TIME IS UP. Do not investigate further. Based ONLY on what you have
already examined, write .forge/review.json NOW:
{"verdict": "pass" or "fail", "summary": "...", "findings": [...]}
If you verified some claims and found no lies, but did not finish
everything, say exactly what was and was not checked in the summary.`

const reviewFixUp = `Your .forge/review.json is INVALID or truncated. Rewrite the COMPLETE
file now — {"verdict": "pass"|"fail", "summary": "...", "findings": [...]} —
valid JSON, nothing else. Do not investigate further.`

// reviewPromptTemplate takes, in order: the request, the claim, the diff, the
// forge's notes, any extra focus, then the first question and the words for
// its answer (reviewBrief.prompt).
const reviewPromptTemplate = `You are an independent REVIEWER. A different agent implemented a change and
wrote a report about it. Your job is adversarial verification: check the
CLAIM against the DIFF and the REQUEST. Do not trust the report.

THE REQUEST:
%s

THE IMPLEMENTER'S CLAIM (verify every statement; do not believe it):
%s

THE ACTUAL DIFF (authoritative):
%s
%s%s
Check, in order:
1. %[6]s
2. Does the claim match the diff? Judge the report's substantive claims:
   a change it describes that the diff does not make, or a change the
   diff makes that it hides or misdescribes, is a finding. AppTown
   itself checks that every changed file is named in the report, so do
   not fail on a file list's omissions alone: a file written by a
   generator the report says it ran (for example "ran rails new"), or
   inside a directory the report names, counts as claimed.
2a. SCOPE: is every change in the diff either needed to satisfy the request
   or explained in the report under "Also changed, and why"? A change that
   is neither — a tidy-up, a rename, a version bump, a .gitignore line, a
   refactor beside the fix — is a finding that fails the review, however
   small or well-meant; name the file and say it must be reverted or
   explained. Workflow-mandated report files are never scope creep. A
   whitespace-only difference (a trailing blank line, line endings) in an
   ignore or config file is not by itself a reason to fail: list it for the
   person instead.
3. Any obvious defects introduced?
4. Hygiene: committed build artifacts, compiled binaries, database files,
   or secrets in the diff are findings — flag them even when everything
   else passes.

MATERIALITY — what justifies a FAIL verdict:
- %[7]s, or
- a change that is neither needed for the request nor explained in the
  report (question 2a), or
- an untrue claim in text this change WROTE or REWROTE (a README that
  names the wrong app, describes features the code does not have, or
  carries a starter's description forward): that is introduced by this
  change, never "pre-existing", because the change chose those words, or
- a defect in the code, or
- the claim misrepresenting what the CODE does, or
- NEW hygiene violations introduced by this change.
If %[8]s and your findings are only about
report completeness regarding incidental files, cleanup of PRE-EXISTING
debt, or accumulation from earlier rework rounds: the verdict is PASS,
with those findings listed for the human. Do not fail the same class of
prose gap twice — humans adjudicate taste; you gate substance.
The current working directory is the changed tree; use your tools for
read-only inspection (reading files, builds, greps) if the diff alone is
not conclusive.

THE FORGE'S OWN CLI is the "forge" command — the platform mounts it first on
PATH, so "forge" is always the platform's CLI, never a package called "forge"
on PyPI or npm (never install one; a different tool with that name has nothing
to say about this change). When the claim says "apptown check" or "apptown config
validate" passed, run "apptown check" yourself (it re-runs this change's declared
gates — guidance, tool checks, AND the source security scans — read-only) and
judge the claim by that.

WRITE the file .forge/review.json containing EXACTLY:
{"verdict": "pass" or "fail", "summary": "one short paragraph", "findings": ["specific problem", ...]}
verdict must be "fail" if %[9]s or the claim
misrepresents the diff. Chat output is ignored; only the file counts.`

func parseReviewVerdict(raw []byte) (reviewVerdict, bool) {
	var v reviewVerdict
	if json.Unmarshal(raw, &v) != nil || (v.Verdict != "pass" && v.Verdict != "fail") {
		return reviewVerdict{}, false
	}
	return v, true
}

// awaitStageBoot waits for the stage's preview step, when it has one, to
// settle — up to the boot's own budget, or until ctx ends — and returns it
// as last read; nil when the stage boots nothing. Polling is cheap next to
// the model call it may save.
func (a *Executor) awaitStageBoot(ctx context.Context, runID ids.ID, stage string) *workflow.Step {
	var boot *workflow.Step
	poll.Until(ctx, 3*time.Minute, 2*time.Second, func() bool {
		steps, err := workflow.ReadStageStepsIn(ctx, a.db, runID, stage)
		if err != nil {
			boot = nil
			return true
		}
		boot = nil
		for _, s := range steps {
			if s.Type == "preview" {
				boot = s
			}
		}
		return boot == nil || (boot.Status != workflow.StatusPending && boot.Status != workflow.StatusRunning)
	})
	return boot
}
