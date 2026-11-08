package verification

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/toolversions"

	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/modelcall"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/strs"
)

// Effects is the evaluator's external work, bound once by the caller. It cannot
// claim or settle workflow work, boot deployments, or read provider credentials.
type Effects interface {
	Run(context.Context, execution.Request) execution.Outcome
	Observe(ctx context.Context, projectID ids.ID, source, probe string) (conformance.Outcome, string)
}

// evaluator translates library judgments into the receipts shared by all checks.
type CheckInvoker interface {
	JudgeCheck(context.Context, check.Item, modelcall.Call, string) CheckResult
}

// Evaluator composes deterministic effects and isolated check invocation.
type Evaluator = evaluator

type evaluator struct {
	agent   CheckInvoker
	effects Effects
	clock   func() time.Time // AppTown's clock, which times a shell check; nil is the real one
	// tools names the shell image sh checks run in: each unit of work (a
	// change check, a guidance step, a review, a what-if, a comparison)
	// takes one snapshot at its start (snapshot) and passes its image down.
	tools toolversions.Source
}

// NewEvaluator runs checks through e; now is AppTown's clock, by which a
// shell check's duration is measured; tools names the images.
func NewEvaluator(e Effects, now func() time.Time, tools toolversions.Source) *evaluator {
	return &evaluator{effects: e, clock: now, tools: tools}
}

// snapshot is the tool versions a unit of work starting now uses
// throughout; with none, it names no image and sh checks error.
func (e *evaluator) snapshot() toolversions.Snapshot {
	if e == nil || e.tools == nil {
		return toolversions.Snapshot{}
	}
	return e.tools.Current()
}
func (e *evaluator) observe(ctx context.Context, projectID ids.ID, source, probe string) (conformance.Outcome, string) {
	return e.effects.Observe(ctx, projectID, source, probe)
}

type llmMetadata struct {
	Pattern     string  `json:"model"`
	Served      string  `json:"servedModel,omitempty"`
	PromptHash  string  `json:"promptHash"`
	Items       int     `json:"items"`
	Cost        float64 `json:"cost,omitempty"`
	Usage       *modelcall.Usage
	NotAssessed map[int]conformance.NotAssessed `json:"-"`
	Results     map[int]CheckResult             `json:"sessions,omitempty"`
}

// judge runs its commands on ctx, the caller's; sh checks in sh, the
// caller's unit's shell image.
func (e *evaluator) judge(ctx context.Context, sh string) *check.Judge {
	return &check.Judge{Run: func(req execution.Request) execution.Outcome { return e.effects.Run(ctx, req) }, Now: e.clock, ShImage: sh}
}
func (e *evaluator) runShChecks(ctx context.Context, sh string, checks []conformance.DeclaredCheck, src, namePrefix string) (results Results, findings []conformance.Finding, shErrors int) {
	for _, d := range declaredAll(checks) {
		if check.Evaluator(d.Declaration) != "sh" {
			continue
		}
		finish := startCheck(ctx, d)
		ev := e.judge(ctx, sh).EvaluateSh([]check.Declared{d}, CheckSource(src), check.Gate, namePrefix)[0]
		res := resultOf(ev)
		res.DurationMs = new(ev.Evidence.Duration.Milliseconds())
		res.EvaluatorVer = evaluatorVersion(ev.Declaration, sh)
		if res.Verdict != workflow.VerdictPass && res.Verdict != workflow.VerdictFail && res.Verdict != workflow.VerdictNotApplicable {
			res.Verdict = workflow.VerdictError // a script that neither passed nor found anything errored
			shErrors++
		}
		findings = append(findings, conformance.WrapFindings(ev.Findings)...)
		results = append(results, res)
		finish(res)
	}
	return results, findings, shErrors
}

// WithCheckJudge binds isolated check execution; every caller uses the same
// operation and capacity pool. Shell and runtime checks are unaffected.
func (e *evaluator) WithCheckJudge(j CheckInvoker) *evaluator { e.agent = j; return e }

func (e *evaluator) judgeChecks(ctx context.Context, call modelcall.Call, items []check.Item, diff string) (map[int]check.Verdict, *llmMetadata, error) {
	meta := &llmMetadata{Pattern: call.Model, Items: len(items), PromptHash: checkTemplateHash(), Results: map[int]CheckResult{}, Usage: &modelcall.Usage{}}
	by := map[int]check.Verdict{}
	for i, it := range items {
		finish := startCheck(ctx, it.Declared)
		var r CheckResult
		if call.Model == "" {
			r.Reason = "no checks model is set up. An administrator must select a checks model"
			r.Evidence.AppTownFault = true
		} else if e.agent == nil {
			r.Reason = "check agent is unavailable. An administrator must check the agent and worker configuration"
			r.Evidence.AppTownFault = true
		} else {
			r = e.agent.JudgeCheck(ctx, it, call, diff)
		}
		receipt := resultOf(check.Judgement(it, r.Verdict, r.assessed(), check.Gate))
		receipt.AgentEvidence = &r.Evidence
		finish(receipt)
		meta.Results[i+1] = r
		meta.Usage.Add(&r.Usage)
		if r.Evidence.ServedModel != "" {
			meta.Served = r.Evidence.ServedModel
		}
		if r.assessed() {
			by[i+1] = r.Verdict
		}
	}
	meta.Cost = meta.Usage.Cost
	return by, meta, nil
}

func declaredAll(checks []conformance.DeclaredCheck) []check.Declared {
	out := make([]check.Declared, 0, len(checks))
	for _, sc := range checks {
		out = append(out, sc.Declared())
	}
	return out
}
func adoptedFor(checks []conformance.DeclaredCheck, d check.Declared) conformance.DeclaredCheck {
	for _, sc := range checks {
		if sc.Block == d.Namespace && sc.Version == d.Version && sc.Check.Name == d.Name {
			return sc
		}
	}
	return conformance.DeclaredCheck{Block: d.Namespace, Version: d.Version, Check: d.Declaration}
}
func llmVerdictDetail(v check.Verdict, meta *llmMetadata, item int) string {
	detail := strings.TrimSpace(v.Note + " [" + v.Evidence + "]")
	if meta != nil {
		served := meta.Served
		if r, ok := meta.Results[item]; ok {
			served = r.Evidence.ServedModel
			if r.Evidence.Reused {
				detail += "\nReused: source and evaluator inputs are unchanged."
			}
		}
		detail += fmt.Sprintf("\n— judged by %s", strs.FirstNonBlank(served, meta.Pattern))
	}
	return detail
}
