package verification

import (
	"context"
	"strings"

	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/strs"

	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/modelcall"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/gitexec"
)

// runLLMChecklist judges every llm rubric in ONE checklist call (conformance_llm.go):
// tree-scoped rubrics on their packed evidence from the proposed tree, diff-
// scoped rubrics on the change's diff. Failures are findings only (never gate)
// while the false-positive rate is being measured. Absent model/slot: checks
// are recorded as skipped — conformance must not depend on a model being up.
func (a *Guidance) runLLMChecklist(ctx context.Context, source workflow.Source, checks []conformance.DeclaredCheck, at checked) (results Results, findings []conformance.Finding, meta map[string]any) {
	src := at.Src
	items := append(check.TreeItems(declaredAll(checks), src), check.DiffItems(declaredAll(checks), "")...)
	if len(items) == 0 {
		return nil, nil, nil
	}
	// An item AppTown could not put to a model at all: the evaluation says
	// cannot-assess, and the receipt word for it is "skipped".
	skipped := func(it check.Item, why string) workflow.CheckReceipt {
		res := resultOf(check.Evaluation{Declared: it.Declared, Mode: check.Gate,
			Outcome: check.CannotAssess, Detail: why})
		res.Scope, res.Verdict, res.Note = it.Scope, workflow.VerdictSkipped, why
		return res
	}
	skipAll := func(why string) {
		for _, it := range items {
			results = append(results, skipped(it, why))
		}
	}
	diff := ""
	needDiff := false
	for _, it := range items {
		if it.Scope == conformance.ScopeDiff {
			needDiff = true
		}
	}
	if needDiff {
		var err error
		diff, err = workflowDiffText(ctx, source, at)
		if err != nil {
			skipAll("reading work diff: " + err.Error())
			return results, nil, nil
		}
		if strings.TrimSpace(diff) == "" {
			// Nothing to judge a transition on; tree items still have their files.
			var keep []check.Item
			for _, it := range items {
				if it.Scope == conformance.ScopeTree {
					keep = append(keep, it)
				} else {
					results = append(results, skipped(it, "no diff to judge"))
				}
			}
			items = keep
			if len(items) == 0 {
				return results, nil, nil
			}
		}
	}
	// Re-hash diff items against the real diff so the record names what was judged.
	for i := range items {
		if items[i].Scope == conformance.ScopeDiff {
			items[i].Hash = check.BundleHash(diff)
		}
	}

	byItem, m, err := a.judgeChecklist(ctx, source, items, diff)
	if m != nil {
		meta = map[string]any{"model": m.Pattern, "promptHash": m.PromptHash, "items": m.Items,
			"packing": check.PackingVersion, "template": check.TemplateHash()}
		if m.Served != "" {
			meta["servedModel"] = m.Served
		}
		if m.ParseError {
			meta["parseError"] = true
		}
	}
	if err != nil {
		skipAll(err.Error())
		return results, nil, meta
	}
	for i, it := range items {
		v, answered := byItem[i+1]
		ev := check.Judgement(it, v, answered, nil, check.Gate)
		res := resultOf(ev)
		// Calibration posture: an llm verdict is recorded and never gates.
		res.Scope, res.Gates = it.Scope, new(bool)
		if !answered {
			// The checklist passed this item over; silence is not a pass.
			res.Verdict, res.Note = workflow.VerdictSkipped, ev.Detail
		} else {
			// The checklist's own word rides on the receipt — "unclear"
			// included, which is a model declining to say. Any other word
			// is a model not saying either (the outcome is cannot-assess).
			res.Verdict, res.Evidence, res.Note = workflow.VerdictUnclear, v.Evidence, v.Note
			if v.Verdict == "pass" || v.Verdict == "fail" {
				res.Verdict, _ = workflow.ParseCheckVerdict(v.Verdict)
			}
		}
		findings = append(findings, conformance.WrapFindings(ev.Findings)...)
		results = append(results, res)
	}
	return results, findings, meta
}

// llmBaseline judges on the change's base tree the tree-scoped rubrics the
// change failed, in one more checklist call, so a rubric main already failed
// is not labelled the change's. Only the failing rubrics are asked, packed
// from the base tree exactly as the change's tree was. A base the model could
// not judge is cannot-assess with the reason, which leaves the change's
// finding undecided. A diff-scoped rubric judges the change itself and has no
// base. No call is made when no tree rubric failed.
func (a *Guidance) llmBaseline(ctx context.Context, source workflow.Source, checks []conformance.DeclaredCheck, findings []conformance.Finding, base string) []check.Evaluation {
	failing := failingChecks(checks, findings, func(d check.Declaration) bool {
		return check.Evaluator(d) == "llm" && check.Scope(d) == conformance.ScopeTree
	})
	if len(failing) == 0 || base == "" {
		return nil
	}
	unassessed := func(why string) []check.Evaluation {
		out := make([]check.Evaluation, 0, len(failing))
		for _, sc := range failing {
			out = append(out, check.Evaluation{Declared: sc.Declared(), Mode: check.Gate, Outcome: check.CannotAssess, Detail: why})
		}
		return out
	}
	repository, err := source.Repository(a.dataDir)
	if err != nil {
		return unassessed(err.Error())
	}
	src, cleanup, err := scm.StageTreeAt(ctx, repository, base)
	defer cleanup()
	if err != nil {
		return unassessed("main's tree at " + strs.ShortSHA(base) + " could not be read: " + err.Error())
	}
	// A rubric that applies to nothing main's contract declares had
	// nothing there to find: known, so the change's finding is its own.
	var out []check.Evaluation
	failing, settled := splitApplicable(failing, "llm", CheckSource(src), check.Gate)
	for _, d := range settled {
		ev := check.Evaluation{Declared: d.Check.Declared(), Mode: check.Gate, Outcome: check.NotApplicable, Detail: d.Receipt.Detail}
		if d.Receipt.Verdict == workflow.VerdictError {
			ev.Outcome = check.CannotAssess
		}
		out = append(out, ev)
	}
	if len(failing) == 0 {
		return out
	}
	items := check.TreeItems(declaredAll(failing), src)
	byItem, _, err := a.judgeChecklist(ctx, source, items, "")
	if err != nil {
		return append(out, unassessed("the model could not judge main's tree: "+err.Error())...)
	}
	for i, it := range items {
		v, answered := byItem[i+1]
		ev := check.Judgement(it, v, answered, nil, check.Gate)
		if ev.Outcome != check.Conforms && ev.Outcome != check.DoesNotConform {
			ev.Outcome, ev.Findings = check.CannotAssess, nil
			ev.Detail = "the model did not judge main's tree: " + strs.FirstNonBlank(ev.Detail, "no verdict")
		}
		out = append(out, ev)
	}
	return out
}

// workflowDiffText is the checked commit's diff from the root's recorded
// fork point, as the diff API compares, read from the repository that holds
// the commit: never the moving branch, and never a project copy that has not
// been given the run's work. Storage errors cannot become an empty diff.
func workflowDiffText(ctx context.Context, source workflow.Source, at checked) (string, error) {
	if !source.RepoBacked() {
		return "", nil
	}
	comparison := source.DefaultBranch + "..." + at.Commit
	if source.RootBaseSHA != "" {
		comparison = source.RootBaseSHA + ".." + at.Commit
	}
	return gitexec.Run(ctx, at.Repo, "diff", comparison)
}

// judgeChecklist asks the source project's secondary fast model, for the run.
func (a *Guidance) judgeChecklist(ctx context.Context, source workflow.Source, items []check.Item, diff string) (map[int]check.Verdict, *llmMetadata, error) {
	if len(items) == 0 {
		return nil, nil, nil
	}
	pattern, _, _, err := a.selection.Resolve(ctx, source.ProjectID, "secondary-fast")
	if err != nil {
		return nil, nil, err
	}
	return a.evaluator.checklist(ctx, modelcall.Call{Purpose: "guidance-checklist", ProjectID: source.ProjectID, RunID: source.RunID, Model: pattern}, items, diff)
}
