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

// runLLMChecklist invokes each rubric independently on the candidate commit.
// Tree checks see the checkout; transition checks also receive the exact diff.
// AI findings are recorded without gating. An unavailable agent or model is
// cannot-assess with its reason, never a passing verdict.
func (a *Guidance) runLLMChecklist(ctx context.Context, source workflow.Source, checks []conformance.DeclaredCheck, at checked) (results Results, findings []conformance.Finding, meta map[string]any) {
	items := append(check.TreeItems(declaredAll(checks)), check.DiffItems(declaredAll(checks), "")...)
	if len(items) == 0 {
		return nil, nil, nil
	}
	// An unavailable check is AppTown's inability to execute, not a fault
	// in the candidate. Preserve that distinction for carried acceptance.
	skipped := func(it check.Item, why string) workflow.CheckReceipt {
		res := resultOf(check.Evaluation{Declared: it.Declared, Mode: check.Gate,
			Outcome: check.CannotAssess, Detail: why, Fault: true})
		res.AgentEvidence = &check.AgentEvidence{AppTownFault: true}
		res.Scope, res.Verdict, res.Note, res.Gates = it.Scope, workflow.VerdictCannotAssess, why, new(false)
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

	for i := range items {
		items[i].Repository, items[i].Commit = at.Repo, at.Commit
	}
	byItem, m, err := a.judgeChecks(ctx, source, items, diff)
	if m != nil {
		meta = map[string]any{"model": m.Pattern, "promptHash": m.PromptHash, "items": m.Items,
			"template": checkTemplateHash(), "sessions": m.Results}
		if m.Served != "" {
			meta["servedModel"] = m.Served
		}
	}
	if err != nil {
		skipAll(err.Error())
		return results, nil, meta
	}
	for i, it := range items {
		v, answered := byItem[i+1]
		ev := check.Judgement(it, v, answered, check.Gate)
		res := resultOf(ev)
		if m != nil {
			r := m.Results[i+1]
			res.AgentEvidence = &r.Evidence
			res.AppTownFault = r.Evidence.AppTownFault
			res.EvaluatorVer = agentEvaluatorVersion(r.Evidence)
			res.EvidenceHash = r.Evidence.Tree
		}
		// Calibration posture: an llm verdict is recorded and never gates.
		res.Scope, res.Gates = it.Scope, new(bool)
		res.Evidence, res.Note, res.Detail = v.Evidence, v.Note, ev.Detail
		if ev.Outcome == check.Conforms || ev.Outcome == check.DoesNotConform {
			res.Detail = llmVerdictDetail(v, m, i+1)
		} else {
			res.Note = ev.Detail
		}
		if m != nil && m.Results[i+1].Reason != "" {
			res.Note, res.Detail = m.Results[i+1].Reason, m.Results[i+1].Reason
		}
		findings = append(findings, conformance.WrapFindings(ev.Findings)...)
		results = append(results, res)
	}
	return results, findings, meta
}

// llmBaseline judges on the change's base tree the tree-scoped rubrics the
// change failed, in separate sessions, so a rubric main already failed
// is not labelled the change's. Only failing rubrics are asked, on the exact
// base checkout. A base the model could
// not judge is cannot-assess with the reason, which leaves the change's
// finding undecided. A diff-scoped rubric judges the change itself and has no
// base. No session is started when no tree rubric failed.
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
			out = append(out, check.Evaluation{Declared: sc.Declared(), Mode: check.Gate, Outcome: check.CannotAssess, Detail: why, Fault: true})
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
	items := check.TreeItems(declaredAll(failing))
	for i := range items {
		items[i].Repository, items[i].Commit = repository, base
	}
	byItem, m, err := a.judgeChecks(ctx, source, items, "")
	if err != nil {
		return append(out, unassessed("the model could not judge main's tree: "+err.Error())...)
	}
	for i, it := range items {
		v, answered := byItem[i+1]
		ev := check.Judgement(it, v, answered, check.Gate)
		if ev.Outcome != check.Conforms && ev.Outcome != check.DoesNotConform {
			ev.Outcome, ev.Findings = check.CannotAssess, nil
			if m != nil && m.Results[i+1].Reason != "" {
				ev.Detail = m.Results[i+1].Reason
			}
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

// judgeChecks uses the source project's checks slot, for the run.
func (a *Guidance) judgeChecks(ctx context.Context, source workflow.Source, items []check.Item, diff string) (map[int]check.Verdict, *llmMetadata, error) {
	if len(items) == 0 {
		return nil, nil, nil
	}
	pattern, _, _, err := a.selection.Resolve(ctx, source.ProjectID, "checks")
	if err != nil {
		return nil, nil, err
	}
	return a.evaluator.judgeChecks(ctx, modelcall.Call{Purpose: "guidance-checklist", ProjectID: source.ProjectID, RunID: source.RunID, Model: pattern}, items, diff)
}
