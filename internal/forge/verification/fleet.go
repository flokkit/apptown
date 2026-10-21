package verification

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/check"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/conformance"
	"flokkit.io/forge/internal/forge/jobs"
	"flokkit.io/forge/internal/forge/modelcall"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/strs"
)

// Fleet posture review (design/subsystems/conformance.md). The estate-wide,
// READ-ONLY posture half of guidance conformance: evaluate every repo-backed project against the ways of working
// it has ADOPTED (its pinned guidance imports), at an exact committed SHA, in
// posture mode, and append an immutable verdict per (project, expectation).
//
// Invariants this file must honour:
//   - It opens no Change and mutates no project, step, or landing gate — it
//     only appends to conformance_reviews / conformance_verdicts (read-only
//     everywhere else).
//   - Conformance is judged against ADOPTED (pinned) guidance, never the
//     instance's latest — collectGuidanceChecks reads pinned imports, so a
//     newly-published version does not retroactively fail an un-adopting
//     project (design/subsystems/conformance.md §1.1). Readiness-for-proposed is a later
//     milestone and a distinct question.
//   - Evaluation is hermetic: an exact committed SHA is archived into an
//     ephemeral temp tree, never the dirty persistent workspace.
//   - Verdicts are immutable records; "current conformance" is a projection
//     over them (currentVerdicts), never a mutated cell.
//
// Evaluation modes (§2): an expectation declares the modes it supports. A
// POSTURE evaluation asks whether the whole committed tree satisfies a
// property, so a check with an `sh` evaluator (deterministic, committed-tree)
// IS posture-assessable. A check whose only evaluator is `llm` is a
// diff-scoped rubric with no posture mode, and yields `cannot-assess` in a
// fleet review — never silently reused from its gate form.

// evaluatorVersion identifies the exact evaluator behind a posture verdict: a
// digest of the pinned image plus the sh script, so a changed script is a
// changed evaluator. Empty when the expectation has no posture evaluator.
func evaluatorVersion(c conformance.CheckDeclaration) string {
	if s, ok := check.PostureScript(c); ok {
		h := sha256.Sum256([]byte(check.ShImage + "\x00" + s))
		return hex.EncodeToString(h[:6])
	}
	if check.Evaluator(c) == "runtime" && strings.TrimSpace(c.Probe) != "" {
		h := sha256.Sum256([]byte("runtime\x00" + c.Probe))
		return hex.EncodeToString(h[:6])
	}
	return ""
}

// stageCommittedTreeAt materialises an exact committed SHA from a repo into an
// ephemeral temp tree (hermetic — not the dirty workspace). Returns the source
// dir and a cleanup func.

const defaultBuildBudget = 6

// The conformance queue's jobs: one runs an admitted review, one an
// admitted what-if, each named by its record's ID. The queue has one
// worker, so reviews and what-ifs run one at a time.
const (
	ReviewJob = "conformance-review"
	WhatIfJob = "what-if"
)

// RunFleetReviewWith admits a review and runs it before returning: for a
// caller that waits on its answer (remediation checking one item).
func (f Fleet) RunFleetReviewWith(ctx context.Context, trigger string, opts conformance.ReviewOptions) (*conformance.Review, []conformance.Verdict, error) {
	execution, err := f.checks.BeginReview(ctx, reviewRequest(trigger, opts))
	if err != nil {
		return nil, nil, err
	}
	rev := execution.Review()
	verdicts, err := f.walkReview(ctx, execution, &rev, opts)
	return &rev, verdicts, err
}

// StartFleetReviewWith admits a review as queued, with the job that runs
// it, and returns the queued record.
func (f Fleet) StartFleetReviewWith(ctx context.Context, trigger string, opts conformance.ReviewOptions) (*conformance.Review, error) {
	return database.Transact(ctx, f.db, func(tx *database.Tx) (*conformance.Review, error) {
		rev, err := f.checks.AdmitReviewIn(ctx, tx, reviewRequest(trigger, opts))
		if err != nil {
			return nil, err
		}
		return rev, f.jobs.EnqueueIn(ctx, tx, jobs.Request{Operation: ReviewJob, Key: rev.ID.String()})
	})
}

// AdoptionTrigger names the reviews an adoption change requests.
const AdoptionTrigger = "adoption"

// RequestAdoptionReviewIn admits, in the adoption's own transaction, a
// queued review of the one project whose way of working it changed, under
// the scope the adoption owner built for that revision (no person's review
// grant is needed or used). Its options are the remediation check's: one
// project, the one-project build allowance.
func (f Fleet) RequestAdoptionReviewIn(ctx context.Context, tx *database.Tx, recorded conformance.AdoptionScope) error {
	opts := conformance.ReviewOptions{Projects: []ids.ID{recorded.ProjectID()}, Build: true, BuildBudget: 1}
	rev, err := f.checks.AdmitReviewIn(conformance.WithAdoption(ctx, recorded), tx, reviewRequest(AdoptionTrigger, opts))
	if err != nil {
		return err
	}
	return f.jobs.EnqueueIn(ctx, tx, jobs.Request{Operation: ReviewJob, Key: rev.ID.String()})
}

func reviewRequest(trigger string, opts conformance.ReviewOptions) conformance.ReviewRequest {
	return conformance.ReviewRequest{Trigger: trigger, Projects: opts.Projects, Build: opts.Build, BuildBudget: opts.BuildBudget}
}

// RunReviewJob runs the review its key names, if this delivery claims it,
// as it was asked. Its outcome is on the review: a delivery that finds it
// claimed or ended does nothing, and a review stopped by shutdown or the
// queue's time limit ends as an error with what it recorded.
func (f Fleet) RunReviewJob(ctx context.Context, key string, _ map[string]any) (map[string]any, error) {
	id, err := ids.Parse(key)
	if err != nil {
		return nil, fmt.Errorf("review job key %q: %w", key, err)
	}
	execution, err := f.checks.ClaimReview(ctx, id)
	if errors.Is(err, conformance.ErrNotQueued) {
		return map[string]any{"review": id, "ran": false}, nil
	}
	if err != nil {
		return nil, err
	}
	rev := execution.Review()
	// The walk records its failure on the review and reports it.
	_, _ = f.walkReview(ctx, execution, &rev, execution.Options())
	return map[string]any{"review": id, "ran": true, "status": rev.Status.String()}, nil
}

// walkReview evaluates an already-created review record, updating its live
// progress as it goes. Shared by the synchronous and background entry points.
func (f Fleet) walkReview(ctx context.Context, execution *conformance.Execution, rev *conformance.Review, opts conformance.ReviewOptions) (verdicts []conformance.Verdict, walkErr error) {
	completed := false
	defer func() {
		rev.Verdicts = len(verdicts)
		status := conformance.ReviewDone
		if !completed && walkErr == nil {
			walkErr = errors.New("review execution interrupted")
		}
		if walkErr != nil {
			status = conformance.ReviewError
			rev.Errors++
		}
		saved, err := execution.Finish(context.WithoutCancel(ctx), conformance.Completion{Projects: rev.Projects, ProjectsDone: rev.ProjectsDone, Errors: rev.Errors, Cost: rev.Cost}, status)
		if err != nil {
			walkErr = errors.Join(walkErr, fmt.Errorf("finishing review %d: %w", rev.ID, err))
			if status == conformance.ReviewDone {
				rev.Errors++
				saved, err = execution.Finish(context.WithoutCancel(ctx), conformance.Completion{Projects: rev.Projects, ProjectsDone: rev.ProjectsDone, Errors: rev.Errors, Cost: rev.Cost}, conformance.ReviewError)
			}
		}
		if err == nil {
			*rev = *saved
		} else {
			walkErr = errors.Join(walkErr, err)
		}
		if walkErr != nil {
			f.reporting.Report("fleet-review", "conformance/fleet-review", walkErr.Error(), map[string]string{"review": fmt.Sprint(rev.ID)}, "")
		}
	}()
	projects, err := execution.Projects(ctx)
	if err != nil {
		return nil, err
	}
	rev.ProjectsTotal = len(projects)
	if err := execution.Progress(ctx, rev.ProjectsDone, rev.CurrentProject); err != nil {
		return nil, err
	}

	budget := opts.BuildBudget
	if budget <= 0 {
		budget = defaultBuildBudget
	}
	buildsUsed := 0

	done := 0
	for _, p := range projects {
		// Advance progress at the start of each project so the activity feed
		// shows what the review is doing right now, not just when it finishes.
		done++
		rev.ProjectsDone, rev.CurrentProject = done, p.Slug
		if err := execution.Progress(ctx, rev.ProjectsDone, rev.CurrentProject); err != nil {
			return verdicts, err
		}
		if p.Branch == "" {
			continue // not repo-backed: no committed posture to assess
		}
		// A project being imported, deleted or kept deleted is not reviewed.
		if l, err := project.LifecycleIn(ctx, f.db, p.ID); err != nil {
			return verdicts, err
		} else if l.State != project.Active {
			continue
		}
		repo := filepath.Join(f.dataDir, "workspaces", "project-"+p.Slug)
		head, err := execution.Source(ctx, p.ID)
		if err != nil {
			return verdicts, err
		}
		sha := head.SHA
		checks := p.Checks // pinned at preparation
		if len(checks) == 0 {
			continue
		}
		rev.Projects++
		exceptions, err := execution.Exceptions(ctx, p.ID)
		if err != nil {
			return verdicts, err
		}

		// Classify by evaluation mode. Posture (sh) checks batch through one
		// staged tree; tool checks run their binding; runtime checks probe a
		// booted app; AI tree rubrics are judged on packed evidence; AI diff
		// rubrics are transitions — not applicable in a review.
		var posture, toolChecks, llmTree []conformance.DeclaredCheck
		for _, sc := range checks {
			switch {
			case check.IsPostureSh(sc.Check):
				posture = append(posture, sc)
			case check.Evaluator(sc.Check) == "tool":
				toolChecks = append(toolChecks, sc)
			case check.IsRuntime(sc.Check):
				st, detail := f.evaluator.Observe(ctx, p.ID, sha, sc.Check.Probe)
				if st == conformance.EvaluationError {
					rev.Errors++
				}
				v, err := f.recordVerdict(ctx, execution, p.ID, sc, st, detail, verdictExtra{})
				if err != nil {
					return verdicts, err
				}
				verdicts = append(verdicts, v)
			case check.Evaluator(sc.Check) == "llm" && check.Scope(sc.Check) == conformance.ScopeTree:
				llmTree = append(llmTree, sc)
			case check.Evaluator(sc.Check) == "llm":
				v, err := f.recordVerdict(ctx, execution, p.ID, sc, conformance.NotApplicable,
					"a transition check: it judges what one change did, so it is judged at landing only", verdictExtra{})
				if err != nil {
					return verdicts, err
				}
				verdicts = append(verdicts, v)
			default:
				v, err := f.recordVerdict(ctx, execution, p.ID, sc, conformance.CannotAssess, "", verdictExtra{})
				if err != nil {
					return verdicts, err
				}
				verdicts = append(verdicts, v)
			}
		}
		if len(posture) == 0 && len(toolChecks) == 0 && len(llmTree) == 0 {
			continue
		}

		src, cleanup, err := scm.StageTreeAt(ctx, repo, sha)
		if err != nil {
			for _, sc := range append(append(append([]conformance.DeclaredCheck{}, posture...), toolChecks...), llmTree...) {
				v, err := f.recordVerdict(ctx, execution, p.ID, sc, conformance.EvaluationError, strs.Truncate(err.Error(), 200), verdictExtra{})
				if err != nil {
					return verdicts, err
				}
				verdicts = append(verdicts, v)
				rev.Errors++
			}
			continue
		}
		err = func() error {
			defer cleanup()
			// Read-only sweep: source scanners run; exec-image tools reuse an image
			// AppTown already holds for this exact commit (a cached conformance
			// build, or the standing deployment's release — by digest), else
			// build only when the review was asked to and the budget allows.
			allowBuild := len(toolChecks) > 0 && opts.Build && buildsUsed < budget
			if allowBuild {
				buildsUsed++
			}
			assessed := f.assessTree(ctx, posture, toolChecks, ToolContext{Context: ctx, Exceptions: append([]conformance.Disposition{}, exceptions...), ProjectID: p.ID, SHA: sha, BuildDir: src, AllowBuild: allowBuild},
				fmt.Sprintf("apptown-fleet-%d-%d", rev.ID, p.ID))
			for _, a := range assessed {
				if a.State == conformance.EvaluationError {
					rev.Errors++
				}
				v, err := f.recordVerdict(ctx, execution, p.ID, a.Check, a.State, a.Detail, verdictExtra{ExceptedBy: a.ExceptedBy})
				if err != nil {
					return err
				}
				verdicts = append(verdicts, v)
			}
			if len(llmTree) > 0 {
				judged, err := f.judgeTreeRubrics(ctx, execution, rev, p.ID, llmTree, src)
				verdicts = append(verdicts, judged...)
				if err != nil {
					return err
				}
			}
			return nil
		}()
		if err != nil {
			return verdicts, err
		}
	}

	completed = true
	return verdicts, nil
}

// judgeTreeRubrics runs ONE checklist call for a project's tree-scoped AI
// rubrics and records a verdict per rubric carrying its evaluator identity and
// evidence hash. No model → cannot-assess with the reason; a fail is recorded
// as does-not-conform, which the projection keeps in the "flagged by AI
// review" lane until a person confirms it (conformance_lens.go).
func (f Fleet) judgeTreeRubrics(ctx context.Context, execution *conformance.Execution, rev *conformance.Review, projectID ids.ID, checks []conformance.DeclaredCheck, src string) ([]conformance.Verdict, error) {
	items := check.TreeItems(declaredAll(checks), src)
	byItem, meta, judgeErr := f.judgeChecklist(ctx, modelcall.Call{Purpose: "conformance-review", ProjectID: projectID}, items, "")
	if meta != nil && meta.Usage != nil {
		rev.Cost += meta.Usage.Cost
	}
	out := []conformance.Verdict{}
	for i, it := range items {
		extra := verdictExtra{EvaluatorVer: check.LLMEvaluatorVersion(it.Declaration), EvidenceHash: it.Hash}
		state, detail := conformance.CannotAssess, "no verdict returned for this item"
		if judgeErr != nil {
			detail = judgeErr.Error()
		} else if v, ok := byItem[i+1]; ok {
			detail = llmVerdictDetail(v, it, meta)
			switch v.Verdict {
			case "pass":
				state = conformance.Conforms
			case "fail":
				state = conformance.DoesNotConform
			}
		}
		v, err := f.recordVerdict(ctx, execution, projectID, adoptedFor(checks, it.Declared), state, detail, extra)
		if err != nil {
			return out, err
		}
		out = append(out, v)
	}
	return out, nil
}

// treeAssessment is one check's outcome on a staged committed tree, with
// what it found. Reason says why a check was not judged here; "" when it
// was asked.
type treeAssessment struct {
	Check      conformance.DeclaredCheck
	State      conformance.Outcome
	Detail     string
	ExceptedBy string
	Reason     conformance.NotAssessed
	Findings   []check.Finding
}

// assessTree runs posture (sh) checks through one staged tree and tool
// checks through their bindings, and says each one's outcome and what it
// found, with the exceptions in tc applied. It records nothing: a review
// records verdicts from it and a what-if and an eval keep their own rows,
// so evaluation is one thing.
func (f Fleet) assessTree(ctx context.Context, posture, tools []conformance.DeclaredCheck, tc ToolContext, label string) []treeAssessment {
	var out []treeAssessment
	if len(posture) > 0 {
		evaluations := f.evaluator.judge(ctx).EvaluateSh(declaredAll(posture), check.Source{Tree: tc.BuildDir}, check.Gate, label)
		for _, sc := range posture {
			a := treeAssessment{Check: sc, State: conformance.EvaluationError, Reason: conformance.NotAssessedError}
			for _, ev := range evaluations {
				if ev.Namespace != sc.Block || ev.Version != sc.Version || ev.Name != sc.Check.Name {
					continue
				}
				result := resultOf(ev)
				switch result.Verdict {
				case workflow.VerdictPass:
					a.State, a.Reason = conformance.Conforms, ""
				case workflow.VerdictFail:
					a.State, a.Reason, a.Detail, a.Findings = conformance.DoesNotConform, "", detailOr(result, sc.Check.Expects), ev.Findings
					if ref, ok := conformance.ExceptionFor(sc, tc.Exceptions); ok {
						// An exception that cannot be named is not applied:
						// the failure stands, with the reason beside it.
						if named, err := strs.JSON(ref); err != nil {
							a.Detail += "; its exception could not be recorded: " + err.Error()
						} else {
							a.State, a.ExceptedBy = conformance.Excepted, named
						}
					}
				default:
					a.Detail = detailOr(result, "")
				}
			}
			out = append(out, a)
		}
	}
	for _, sc := range tools {
		ev, verdict, exceptedBy := f.scanners.evalToolCheck(ctx, sc, tc, label)
		detail := func(fallback string) string {
			if ev.Detail != "" {
				return strs.Truncate(ev.Detail, 300)
			}
			return fallback
		}
		a := treeAssessment{Check: sc, State: conformance.EvaluationError, Reason: conformance.NotAssessedError, Detail: detail("")}
		switch verdict {
		case workflow.VerdictPass:
			a.State, a.Reason, a.Findings = conformance.Conforms, "", ev.Findings
		case workflow.VerdictFail:
			a.State, a.Reason, a.Detail, a.Findings = conformance.DoesNotConform, "", detail(sc.Check.Expects), ev.Findings
		case workflow.VerdictExcepted:
			a.State, a.Reason, a.Detail, a.ExceptedBy, a.Findings = conformance.Excepted, "", detail(sc.Check.Expects), exceptedBy, ev.Findings
		case workflow.VerdictSkipped:
			a.State, a.Reason, a.Detail = conformance.CannotAssess, conformance.NotAssessedNoImage, detail("exec-image tool runs at the land gate")
		}
		out = append(out, a)
	}
	return out
}

// verdictExtra carries the optional identity facts a verdict records.
type verdictExtra struct {
	ExceptedBy   string
	EvidenceHash string
	EvaluatorVer string // overrides evaluatorVersion(check) when set (AI checks)
}

// recordVerdict appends one immutable verdict and returns it.

func (f Fleet) recordVerdict(ctx context.Context, execution *conformance.Execution, projectID ids.ID, sc conformance.DeclaredCheck, state conformance.Outcome, detail string, x verdictExtra) (conformance.Verdict, error) {
	pending, err := execution.PrepareVerdict(ctx, conformance.Assessment{
		ProjectID: projectID, Check: conformance.CheckIdentity{Block: sc.Block, Version: sc.Version, Check: sc.Check.Name},
		State: state, Detail: detail, EvaluatorVersion: strs.FirstNonBlank(x.EvaluatorVer, evaluatorVersion(sc.Check)), ExceptedBy: x.ExceptedBy, EvidenceHash: x.EvidenceHash,
	})
	if err != nil {
		return conformance.Verdict{}, err
	}
	var fix *conformance.FixAttribution
	if pending.NeedsAttribution() {
		fix, err = f.prepareVerdictFix(ctx, pending.Previous(), pending.Verdict())
		if err != nil {
			return conformance.Verdict{}, err
		}
	}
	return pending.Commit(ctx, fix)
}
