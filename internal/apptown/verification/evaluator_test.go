package verification

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/modelcall"
	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/stock"
)

// timedRun runs every command in seven seconds of AppTown's clock.
type timedRun struct {
	Effects
	at *atomic.Int64
}

func (r timedRun) Run(context.Context, execution.Request) execution.Outcome {
	r.at.Add(int64(7 * time.Second))
	return execution.Outcome{}
}

// A shell check's reported duration is measured on AppTown's clock, which
// the evaluator gives its judge.
func TestAShellChecksDurationIsAppTownsClock(t *testing.T) {
	t.Parallel()
	var at atomic.Int64
	at.Store(time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC).UnixNano())
	e := NewEvaluator(timedRun{at: &at}, func() time.Time { return time.Unix(0, at.Load()) }, stock.Versions())
	sc := conformance.DeclaredCheck{Block: "b", Version: 1, Check: conformance.CheckDeclaration{AppliesTo: []string{"repository"}, Name: "quick", Expects: "e", Evaluator: "sh", Sh: "exit 0", Severity: "low"}}
	tree := t.TempDir()
	writeTree(t, tree, map[string]string{"README.md": "x"})
	results, _, _ := e.runShChecks(context.Background(), stock.Images().Sh, []conformance.DeclaredCheck{sc}, tree, "t-clock")
	if len(results) != 1 || results[0].DurationMs == nil || *results[0].DurationMs != 7000 {
		t.Fatalf("a check seven seconds long on AppTown's clock: %+v", results)
	}
}

func TestWhatIfChecksObserveSpendBetweenSessions(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		calls := 0
		spend := conformance.WhatIfSpend{}
		judge := budgetedRubrics(conformance.WhatIfSpend{}, 500000, &spend, func(items []check.Item, _ string) (map[int]check.Verdict, *llmMetadata, error) {
			calls++
			if len(items) != 1 {
				t.Fatal("checks were batched")
			}
			u := &modelcall.Usage{Cost: 0.8}
			if unknown {
				u.Cost = 0
				u.Incomplete = "provider usage missing"
			}
			return map[int]check.Verdict{1: {Verdict: "pass"}}, &llmMetadata{Usage: u, Results: map[int]CheckResult{1: {Verdict: check.Verdict{Verdict: "pass"}}}}, nil
		})
		by, meta, err := judge([]check.Item{{}, {}}, "")
		if err != nil || calls != 1 || len(by) != 1 || meta.Results[2].Reason == "" || meta.NotAssessed[2] == "" {
			t.Fatalf("allowance was not checked between sessions: calls=%d by=%v meta=%+v err=%v", calls, by, meta, err)
		}
	}
}
