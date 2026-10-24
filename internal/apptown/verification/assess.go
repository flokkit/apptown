package verification

import (
	"context"
	"github.com/flokkit/apptown/internal/apptown/workflow"

	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/strs"
)

// One assessment of declared checks against one committed tree
// (design/subsystems/conformance.md, "Pack versions and what-if"): what a what-if
// and an eval's comparison both ask. It answers each check with its
// outcome and what it found, or says why it was not judged here, and it
// records nothing: each caller keeps its own rows.

// assessInput is what an assessment may use beyond the tree.
type assessInput struct {
	Tool ToolContext // the tree, its commit, exceptions and whether an image may be built
	// Sh is the shell image of the unit's tool versions snapshot: one
	// what-if or one comparison uses one for every tree it judges.
	Sh string
	// Label names the containers the checks run in.
	Label string
	// Running is the app serving exactly this commit, when one does:
	// runtime probes are judged against it. Without it they are not
	// assessed, for NoApp's reason and detail.
	Running *runningApp
	NoApp   string
	// Judge judges model rubrics, one checklist for all of them; nil leaves
	// them not assessed for NoJudge. Diff is the change a diff rubric
	// judges; without one a diff rubric judges nothing here.
	Judge   func(items []check.Item, diff string) (map[int]check.Verdict, *llmMetadata, error)
	NoJudge conformance.NotAssessed
	Diff    string
}

// runningApp is a deployment serving the commit, and a way to ask that it
// still does after each probe.
type runningApp struct {
	Target Target
	Still  func() bool
}

// assessChecks answers every check once. Shell and tool checks run on the
// tree (an image tool builds within the caller's allowance); runtime probes
// ask the running app; model rubrics go to the judge in one checklist; a
// check nothing here can judge says why.
func (f Fleet) assessChecks(ctx context.Context, checks []conformance.DeclaredCheck, in assessInput) []treeAssessment {
	var posture, tools, rubrics, runtime []conformance.DeclaredCheck
	var out []treeAssessment
	notJudged := func(sc conformance.DeclaredCheck, why conformance.NotAssessed, detail string) {
		out = append(out, treeAssessment{Check: sc, State: conformance.CannotAssess, Reason: why, Detail: detail})
	}
	// Rubrics and probes judged once for the app are settled first when they
	// apply to nothing the tree's contract declares (what-if, an eval).
	if in.Tool.BuildDir != "" {
		src := CheckSource(in.Tool.BuildDir)
		var settled, more []decided
		checks, settled = splitApplicable(checks, "runtime", src, check.Posture)
		checks, more = splitApplicable(checks, "llm", src, check.Posture)
		for _, d := range append(settled, more...) {
			state := conformance.NotApplicable
			if d.Receipt.Verdict == workflow.VerdictError {
				state = conformance.EvaluationError
			}
			out = append(out, treeAssessment{Check: d.Check, State: state, Detail: d.Receipt.Detail})
		}
	}
	for _, sc := range checks {
		switch {
		case check.IsPostureSh(sc.Check):
			posture = append(posture, sc)
		case check.Evaluator(sc.Check) == "tool":
			tools = append(tools, sc)
		case check.IsRuntime(sc.Check):
			runtime = append(runtime, sc)
		case check.Evaluator(sc.Check) == "llm" && (check.Scope(sc.Check) == conformance.ScopeTree || in.Diff != ""):
			rubrics = append(rubrics, sc)
		case check.Evaluator(sc.Check) == "llm":
			notJudged(sc, conformance.NotAssessedJudgesAChange, "")
		default:
			notJudged(sc, conformance.NotAssessedNoEvaluator, "")
		}
	}
	if len(posture)+len(tools) > 0 {
		out = append(out, f.assessTree(ctx, in.Sh, posture, tools, in.Tool, in.Label)...)
	}
	var probe func(context.Context, Target, string) check.ProbeVerdict
	if f.runtime != nil {
		probe = f.runtime.Probe
	}
	for _, sc := range runtime {
		if ps, err := check.ParseProbe(sc.Check.Probe); err == nil && !ps.NeedsApp() && in.Tool.BuildDir != "" && probe != nil {
			// The tree being assessed answers it; no running app is needed.
			out = append(out, probeRunning(ctx, sc, runningApp{Target: Target{ProjectID: in.Tool.ProjectID, ContractDir: in.Tool.BuildDir}, Still: func() bool { return true }}, probe))
			continue
		}
		if in.Running == nil {
			notJudged(sc, conformance.NotAssessedNoRunningApp, in.NoApp)
			continue
		}
		out = append(out, probeRunning(ctx, sc, *in.Running, probe))
	}
	if len(rubrics) > 0 {
		out = append(out, judgeRubrics(rubrics, in)...)
	}
	return out
}

// probeRunning judges one runtime probe against the app serving the
// commit. An app that changed while it was probed answered for another
// commit, so its answer is not this one's.
func probeRunning(ctx context.Context, sc conformance.DeclaredCheck, app runningApp, probe func(context.Context, Target, string) check.ProbeVerdict) treeAssessment {
	a := treeAssessment{Check: sc, State: conformance.CannotAssess, Reason: conformance.NotAssessedError}
	if probe == nil {
		a.Reason, a.Detail = conformance.NotAssessedNoRunningApp, "AppTown cannot observe deployments here"
		return a
	}
	v := probe(ctx, app.Target, sc.Check.Probe)
	switch {
	case !app.Still():
		a.Reason, a.Detail = conformance.NotAssessedNoRunningApp, "the deployment changed while it was being probed"
	case v.Outcome == check.Conforms:
		a.State, a.Reason, a.Detail = conformance.Conforms, "", v.Detail
	case v.Outcome == check.DoesNotConform:
		a.State, a.Reason, a.Detail = conformance.DoesNotConform, "", v.Detail
		a.Findings = []check.Finding{{Fingerprint: conformance.Fingerprint(sc, ""), Kind: "conformance", Severity: sc.Check.Severity, Title: sc.Check.Name, Detail: v.Detail, Source: "guidance"}}
	default:
		a.Detail = v.Detail
	}
	return a
}

// judgeRubrics sends every rubric in one checklist, or none: without a
// judge each says why, and a call that failed leaves each one unanswered
// with the provider's words.
func judgeRubrics(rubrics []conformance.DeclaredCheck, in assessInput) []treeAssessment {
	var out []treeAssessment
	if in.Judge == nil {
		for _, sc := range rubrics {
			out = append(out, treeAssessment{Check: sc, State: conformance.CannotAssess, Reason: in.NoJudge})
		}
		return out
	}
	declared := declaredAll(rubrics)
	items := append(check.TreeItems(declared, in.Tool.BuildDir), check.DiffItems(declared, in.Diff)...)
	byItem, meta, err := in.Judge(items, in.Diff)
	for i, it := range items {
		sc := adoptedFor(rubrics, it.Declared)
		if err != nil {
			out = append(out, treeAssessment{Check: sc, State: conformance.CannotAssess, Reason: conformance.NotAssessedError, Detail: strs.Truncate(err.Error(), 200)})
			continue
		}
		v, answered := byItem[i+1]
		ev := check.Judgement(it, v, answered, nil, check.Gate)
		a := treeAssessment{Check: sc, State: outcomeOf(ev.Outcome), Detail: llmVerdictDetail(v, it, meta), Findings: ev.Findings}
		if ev.Outcome != check.Conforms && ev.Outcome != check.DoesNotConform {
			a.State, a.Reason, a.Detail = conformance.CannotAssess, conformance.NotAssessedError, ev.Detail
		}
		out = append(out, a)
	}
	return out
}
