package workflowexec

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/dockerlabel"

	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/strs"

	"github.com/flokkit/apptown/internal/apptown/repositoryedit"

	piharness "github.com/flokkit/apptown/internal/apptown/agentharness/pi"

	"github.com/flokkit/apptown/internal/apptown/workflow"
)

type agentRecord struct {
	task     *workflow.Execution
	cfg      *agentConfig
	pi       piResult
	files    workspaceHarvest
	steering steeringFile

	modelSlot, modelProfile string
	executor, image         string
	scmGranted              string
	start                   time.Time
	queuedMs                int64
	attempts                int
	// worker is set for an attempt that ran on the worker: its result was
	// published (or kept refused) before its outcome is recorded, and its
	// image is the worker's.
	worker *workerRecord
}

// recordAgentOutcome decides the step's status, writes its evidence, commits
// the work, and records a pass together with its output and file artifacts.
// It returns the status recorded: an error when the outcome could not be.
// Evidence keys are read by name elsewhere (console, UI, feedback) — keep them.
func (a *Executor) recordAgentOutcome(ctx context.Context, r agentRecord) (workflow.StepState, error) {
	// The agent has run: its outcome is judged and recorded though the
	// caller stops, so a stop never reads as "nothing could be read".
	ctx = context.WithoutCancel(ctx)
	step, run := r.task.Step(), r.task.Run()
	out := r.pi.Output
	status := workflow.StatusPass
	reason := ""
	if r.pi.TimedOut {
		status, reason = workflow.StatusError, "timed out"
	} else if r.pi.RunErr != nil {
		status, reason = workflow.StatusError, r.pi.RunErr.Error()
	} else if r.worker != nil && r.worker.problem != "" {
		// A declared result that could not be read is not "no output".
		status, reason = workflow.StatusError, r.worker.problem
	} else if r.files.Problem != "" {
		status, reason = workflow.StatusError, r.files.Problem
	} else if r.files.MissingOutput != "" {
		// The declared output is the output: nothing else stands in for it.
		status, reason = workflow.StatusFail, "the step declares its output in "+r.files.MissingOutput+", and the agent did not write it (or left it empty)"
	} else if strings.TrimSpace(r.files.Output) == "" {
		if r.worker != nil && r.worker.outputUnread != "" {
			status, reason = workflow.StatusError, "the agent's output could not be read: "+r.worker.outputUnread
		} else if perr := piharness.LastModelError(out); perr != "" {
			// Infrastructure, not the agent's work: an error, and the
			// provider's own words ('credit balance too low' must not read
			// as 'agent produced no output').
			status, reason = workflow.StatusError, "the model provider refused the request: "+strs.Truncate(perr, 300)
		} else {
			status, reason = workflow.StatusFail, "agent produced no output (no text, no files)"
		}
	}

	display := r.files.Output
	if len(display) > maxAgentOutput {
		display = display[:maxAgentOutput] + "\n... (truncated; full text in artifact)"
	}
	var wroteNames []string
	for _, f := range r.files.Files {
		wroteNames = append(wroteNames, f.rel)
	}
	// The image's labels say which runtimes ran: the worker's own image's
	// on a worker, never AppTown's Docker's.
	label := func(key string) string { return a.docker.AgentImageLabel(ctx, r.image, key) }
	if r.worker != nil {
		label = func(key string) string { return r.worker.image.Labels[key] }
	}
	evidence := agentEvidence{
		Model:            r.pi.Model,
		ModelSlot:        r.modelSlot,
		ModelProfile:     r.modelProfile,
		PiVersion:        label(dockerlabel.PiVersion),
		SteeringHash:     r.steering.Hash,
		ScmAccess:        r.scmGranted,
		GlabVersion:      label(dockerlabel.GlabVersion),
		SubagentsVersion: label(piharness.SubagentsLabel),
		Provider:         r.pi.Provider,
		Usage:            r.pi.Usage,
		UsageIncomplete:  r.pi.UsageIncomplete,
		Output:           display,
		Files:            wroteNames,
		DurationMs:       a.now().Sub(r.start).Milliseconds(),
		QueuedMs:         r.queuedMs,
		Attempts:         r.attempts,
		Executor:         r.executor,
		GuidanceHash:     r.steering.GuidanceHash,
		Skills:           r.steering.Skills,
		Resources:        r.steering.Resources,
	}
	if r.executor == execution.Docker {
		evidence.Image = r.image
	}
	if w := r.worker; w != nil {
		evidence.Worker, evidence.Source, evidence.UsageUnknown, evidence.PrivateDocker, evidence.WorkerProfile = w.backend, w.source, w.outputUnread, w.privateDocker, w.profile
		if w.candidate.Ref != "" {
			evidence.Result = &taskResultEvidence{Ref: w.candidate.Ref, Commit: w.candidate.Tip, Refused: w.candidate.Refused}
		}
	}
	if reason != "" {
		evidence.Reason = reason
		if len(out) > 0 {
			tail := string(out)
			if len(tail) > 4096 {
				tail = tail[len(tail)-4096:]
			}
			evidence.StderrTail = tail
		}
	}
	// The agent's work is committed and published before the step's
	// outcome is recorded: a pass means the work is shared (collecting
	// before pass).
	var published error
	if r.worker != nil {
		published = r.worker.published
	} else {
		published = repositoryedit.CommitStep(ctx, a.db, a.cfg.DataDir, a.branches, a.publisher, run, step, a.now())
	}
	if published != nil && status == workflow.StatusPass {
		status = workflow.StatusError
		evidence.Reason = "the step's work could not be published: " + published.Error()
	}
	noChange := false
	if status == workflow.StatusPass {
		switch empty, adoption, err := a.leftNoChange(ctx, run, step, r.cfg); {
		case err != nil:
			status, evidence.Reason = workflow.StatusError, "whether the step changed anything could not be read: "+err.Error()
		case empty && adoption:
			// An adoption that needs no code: the landing is the witness
			// (its conformance gate judges each new check on the tree),
			// and it still moves the project to the target. Not
			// nothingToChange, which would close the change unlanded and
			// leave the project where it was.
			evidence.Reason = adoptionNeedsNoCode(step.Name, r.files.Output)
		case empty && r.cfg.MayChangeNothing:
			// An empty result is this step's declared complete answer: it
			// passes, and the run ends at its approval without asking
			// anyone to accept an empty change.
			evidence.NothingToChange, evidence.Reason = true, nothingToChange(step.Name, r.files.Output)
		case empty:
			status, evidence.Reason, noChange = workflow.StatusFail, madeNoChange(r.files.Output), true
		}
	}
	outcome, encodeErr := workflow.EncodeOutcome(status, evidence, map[string]any{"model": r.pi.Model, "provider": r.pi.Provider})
	switch {
	case status == workflow.StatusPass:
		outcome.Artifacts = agentArtifacts(r.cfg, r.files, step.Name)
	case noChange:
		// The report is the agent's account of why it changed nothing: the
		// stage's reviewer and a person read it (a bug that could not be
		// reproduced is an honest stop, stock/fix-bug.yaml).
		outcome.Artifacts = agentArtifacts(r.cfg, r.files, step.Name)[:1]
	}
	// The invocation and the publication are done; only the recording is
	// tried again if it fails (settleProduced).
	return a.settleProducedStep(r.task, status, func() error { return r.task.Finish(outcome, encodeErr) })
}

// agentArtifacts are a passing agent step's products: the typed output a
// later step consumes by kind, and every workspace file the agent wrote (the
// materialization substrate, the "browse" rung). They are recorded with the
// passing outcome, so no reader sees the pass without them.
func agentArtifacts(cfg *agentConfig, files workspaceHarvest, stepName string) []workflow.StepArtifact {
	var out []workflow.StepArtifact
	if cfg.Output.Kind != "" {
		name := cfg.Output.Name
		if name == "" {
			name = stepName
		}
		out = append(out, workflow.StepArtifact{ArtifactDraft: workflow.ArtifactDraft{Kind: cfg.Output.Kind, Name: name, Body: files.Output}, By: "agent:" + stepName})
	}
	for _, f := range files.Files {
		if files.MdFile != "" && f.rel == files.MdFile && cfg.Output.Kind != "" {
			continue // already attached as the typed output artifact
		}
		out = append(out, workflow.StepArtifact{ArtifactDraft: workflow.ArtifactDraft{Kind: "file", Name: f.rel, Body: f.body}})
	}
	return out
}

// leftNoChange: an implementing step whose change is still empty after it
// has published. A step implements when its stage reviews its report
// against the diff (an agent/review step "of" its output kind): the report
// is a claim about a change, so with no change there is nothing for it to
// claim. Such a step that committed nothing would otherwise pass, and the
// run fail later at the image build. It is the CHANGE that must not be empty, not this session's commits: a rework
// round that only corrects its report has still done its part. Steps that
// legitimately change nothing (research, a plan, a spec) are not reviewed
// against a diff, and pass as before. A step that declares an empty result
// a complete answer (mayChangeNothing) is always asked, reviewed or not: an
// empty change is then how its run ends. An error is "could not be read",
// never a pass.
//
// adoption says the empty change is an adoption Change, whose landing has
// something to do without code (workflow.Change.LandsWithoutCode).
func (a *Executor) leftNoChange(ctx context.Context, run *workflow.Run, step *workflow.Step, cfg *agentConfig) (empty, adoption bool, err error) {
	if !cfg.MayChangeNothing {
		if cfg.Output.Kind == "" {
			return false, false, nil
		}
		stage, err := workflow.ReadStageStepsIn(ctx, a.db, run.ID, step.Stage)
		if err != nil {
			return false, false, err
		}
		if !reviewsAgainstTheDiff(stage, cfg.Output.Kind) {
			return false, false, nil
		}
	}
	source, err := workflow.ReadSourceIn(ctx, a.db, workflow.RunRef{ID: run.ID, ProjectID: run.ProjectID})
	if err != nil {
		return false, false, err
	}
	if empty, err = workflow.ChangeIsEmpty(ctx, a.cfg.DataDir, source); err != nil || !empty {
		return empty, false, err
	}
	change, err := workflow.ReadChangeInProject(ctx, a.db, workflow.ChangeRef{ID: source.ChangeID, ProjectID: source.ProjectID})
	if err != nil {
		return false, false, err
	}
	return true, change.LandsWithoutCode(), nil
}

// reviewsAgainstTheDiff: one of the stage's steps is an agent/review of kind.
func reviewsAgainstTheDiff(stage []*workflow.Step, kind string) bool {
	for _, s := range stage {
		var rc reviewConfig
		if s.Type == ReviewKind && json.Unmarshal(s.Config, &rc) == nil && rc.Of == kind {
			return true
		}
	}
	return false
}

// nothingToChange is the account of a step that declared an empty result
// a complete answer and changed nothing, with its report.
func nothingToChange(step, report string) string {
	reason := "nothing to change: the " + step + " step found nothing to change"
	if report = strings.TrimSpace(report); report != "" {
		reason += ". Its report: " + strs.Truncate(report, 600)
	}
	return reason
}

// adoptionNeedsNoCode is the account of an implementing step that left an
// adoption Change empty, with its report.
func adoptionNeedsNoCode(step, report string) string {
	reason := "nothing to change: the " + step + " step found that this adoption needs no code change; the landing judges the adoption's acceptance checks on the tree before the project moves"
	if report = strings.TrimSpace(report); report != "" {
		reason += ". Its report: " + strs.Truncate(report, 600)
	}
	return reason
}

// madeNoChange is the reason an implementing step that changed nothing
// fails, with the agent's own explanation when it wrote one: a step that
// could not do the work (a bug it could not reproduce) says why, and a
// person reads that, not another round of the same work.
func madeNoChange(report string) string {
	reason := "the step made no change: its report is reviewed against the change's diff, and the change has nothing committed"
	if report = strings.TrimSpace(report); report != "" {
		reason += ". The agent's report: " + strs.Truncate(report, 600)
	}
	return reason
}
