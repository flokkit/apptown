package verification

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/modelcall"
	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/stock"
)

// The test supplies only completion; an unexpected command or runtime probe
// fails instead of silently providing another service through the fixture.
type checklistEffects struct {
	Effects
	complete func(string, string, string) (string, *modelcall.Usage, error)
}

func (e checklistEffects) Ask(_ context.Context, c modelcall.Call) (string, *modelcall.Usage, error) {
	return e.complete(c.Model, c.System, c.User)
}
func TestChecklistAsksAgainForJSON(t *testing.T) {
	t.Parallel()
	calls := 0
	e := NewEvaluator(checklistEffects{complete: func(pattern, system, user string) (string, *modelcall.Usage, error) {
		calls++
		if calls == 1 {
			return "Sure! Here is my assessment in prose.", &modelcall.Usage{}, nil
		}
		if !strings.Contains(user, "JSON array only") {
			t.Errorf("the second ask says what was wrong")
		}
		return `[{"item":1,"verdict":"conforms","detail":"fine"}]`, &modelcall.Usage{}, nil
	}}, nil, stock.Versions())
	sc := conformance.DeclaredCheck{Block: "b", Version: 1, Check: conformance.CheckDeclaration{AppliesTo: []string{"repository"}, Name: "c", Expects: "e", Rubric: "r", Evaluator: "llm", Scope: "diff"}}
	items := check.DiffItems(declaredAll([]conformance.DeclaredCheck{sc}), "diff")
	by, meta, err := e.Checklist(context.Background(), modelcall.Call{Purpose: "guidance-checklist", Model: "openrouter/test-model"}, items, "diff")
	if err != nil || meta == nil || len(by) != 1 || !meta.Retried || calls != 2 {
		t.Fatalf("want one retried verdict from two calls: err=%v by=%v meta=%+v calls=%d", err, by, meta, calls)
	}
}

// A checklist call that failed after it was sent still owes its spend: the
// usage comes back with the error, so the guidance check records it as
// unknown rather than nothing, and a retry's spend adds to the first's.
func TestChecklistKeepsTheSpendOfAFailedCall(t *testing.T) {
	t.Parallel()
	calls := 0
	e := NewEvaluator(checklistEffects{complete: func(pattern, system, user string) (string, *modelcall.Usage, error) {
		calls++
		if calls == 1 {
			return "prose, not JSON", &modelcall.Usage{Prompt: 4, Completion: 2, Cost: 0.5}, nil
		}
		return "", &modelcall.Usage{Incomplete: "the call ended after it was sent"}, errors.New("timeout")
	}}, nil, stock.Versions())
	sc := conformance.DeclaredCheck{Block: "b", Version: 1, Check: conformance.CheckDeclaration{AppliesTo: []string{"repository"}, Name: "c", Expects: "e", Rubric: "r", Evaluator: "llm", Scope: "diff"}}
	items := check.DiffItems(declaredAll([]conformance.DeclaredCheck{sc}), "diff")
	_, meta, err := e.Checklist(context.Background(), modelcall.Call{Purpose: "guidance-checklist", Model: "openrouter/test-model"}, items, "diff")
	if err == nil || meta == nil || meta.Usage == nil || meta.Usage.Incomplete == "" || meta.Usage.Prompt != 4 || meta.Usage.Cost != 0.5 {
		t.Fatalf("spend of a failed retry kept and marked unknown: err=%v meta=%+v", err, meta)
	}
}

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
