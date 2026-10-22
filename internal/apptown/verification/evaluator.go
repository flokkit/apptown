package verification

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/ids"

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
	// Ask is AppTown's own model call (inference.Gateway.Ask), which
	// admits it, sends it and writes its one spend line.
	Ask(ctx context.Context, call modelcall.Call) (string, *modelcall.Usage, error)
	Observe(ctx context.Context, projectID ids.ID, source, probe string) (conformance.Outcome, string)
}

// Evaluator translates library judgments into the receipts shared by all checks.
type Evaluator struct {
	effects Effects
	clock   func() time.Time // AppTown's clock, which times a shell check; nil is the real one
}

// NewEvaluator runs checks through e; now is AppTown's clock, by which a
// shell check's duration is measured.
func NewEvaluator(e Effects, now func() time.Time) *Evaluator {
	return &Evaluator{effects: e, clock: now}
}
func (e *Evaluator) Observe(ctx context.Context, projectID ids.ID, source, probe string) (conformance.Outcome, string) {
	return e.effects.Observe(ctx, projectID, source, probe)
}

type llmMetadata struct {
	Pattern    string  `json:"model"`
	Served     string  `json:"servedModel,omitempty"`
	PromptHash string  `json:"promptHash"`
	Items      int     `json:"items"`
	Cost       float64 `json:"cost,omitempty"`
	Usage      *modelcall.Usage
	ParseError bool `json:"parseError,omitempty"`
	Retried    bool `json:"retried,omitempty"` // the answer was asked for twice: the first was not JSON
	Truncated  bool `json:"bundleTruncated,omitempty"`
}

// judge runs its commands on ctx, the caller's.
func (e *Evaluator) judge(ctx context.Context) *check.Judge {
	return &check.Judge{Run: func(req execution.Request) execution.Outcome { return e.effects.Run(ctx, req) }, Now: e.clock}
}
func (e *Evaluator) runShChecks(ctx context.Context, checks []conformance.DeclaredCheck, src, namePrefix string) (results Results, findings []conformance.Finding, shErrors int) {
	for _, ev := range e.judge(ctx).EvaluateSh(declaredAll(checks), check.Source{Tree: src}, check.Gate, namePrefix) {
		res := resultOf(ev)
		res.DurationMs = new(ev.Evidence.Duration.Milliseconds())
		if res.Verdict != workflow.VerdictPass && res.Verdict != workflow.VerdictFail {
			res.Verdict = workflow.VerdictError // a script that neither passed nor found anything errored
			shErrors++
		}
		findings = append(findings, conformance.WrapFindings(ev.Findings)...)
		results = append(results, res)
	}
	return results, findings, shErrors
}

// Checklist judges items in one checklist question, asked as call (its
// purpose, project, run and model; Judge for an eval comparison's rubric).
// Each paid attempt writes its own spend line in the gateway; the metadata
// adds up what the attempts cost, failed ones included, for callers' own
// budgets.
func (e *Evaluator) Checklist(ctx context.Context, call modelcall.Call, items []check.Item, diff string) (map[int]check.Verdict, *llmMetadata, error) {
	if call.Model == "" {
		return nil, nil, fmt.Errorf("no fast model slot bound")
	}
	meta := &llmMetadata{Pattern: call.Model}
	judge := &check.Judge{Complete: check.CompleterFunc(func(ctx context.Context, system, user string) (check.Completion, error) {
		c := call
		c.System, c.User = system, user
		out, usage, err := e.effects.Ask(ctx, c)
		// Spend is owed even when the call failed after it was sent.
		if usage != nil {
			if meta.Usage == nil {
				meta.Usage, meta.Served = usage, usage.Model
			} else {
				meta.Usage.Add(usage)
			}
			meta.Cost = meta.Usage.Cost
		}
		if err != nil {
			return check.Completion{}, err
		}
		return check.Completion{Text: out, Model: meta.Served}, nil
	})}
	by, m, err := judge.Checklist(ctx, items, diff)
	if m != nil {
		meta.PromptHash, meta.Items, meta.ParseError, meta.Retried = m.PromptHash, m.Items, m.ParseError, m.Retried
	}
	return by, meta, err
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
func llmVerdictDetail(v check.Verdict, it check.Item, meta *llmMetadata) string {
	var parts []string
	if strings.TrimSpace(v.Note) != "" {
		parts = append(parts, strings.TrimSpace(v.Note))
	}
	if strings.TrimSpace(v.Evidence) != "" {
		parts = append(parts, "["+strings.TrimSpace(v.Evidence)+"]")
	}
	s := strings.Join(parts, " ")
	if meta != nil {
		served := meta.Served
		if served == "" {
			served = meta.Pattern
		}
		s += fmt.Sprintf("\n— judged by %s", served)
		if len(it.Files) > 0 {
			s += fmt.Sprintf(" on %d file(s): %s", len(it.Files), strs.Truncate(strings.Join(it.Files, ", "), 200))
		}
	}
	return strings.TrimSpace(s)
}
