package verification

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/modelcall"
	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/ids/idstest"
	"github.com/flokkit/apptown/internal/progress"
)

type progressInvoker func(context.Context, check.Item, modelcall.Call, string) CheckResult

func (f progressInvoker) JudgeCheck(ctx context.Context, it check.Item, call modelcall.Call, diff string) CheckResult {
	return f(ctx, it, call, diff)
}

// Every reused rubric still starts and finishes in order. The start is visible
// while its evaluator is executing, and the duration measures this invocation.
func TestReusedChecksReportLivePositionAndDuration(t *testing.T) {
	t.Parallel()
	var feeds progress.Feeds
	id := idstest.N(1)
	feeds.Start(id)
	now := time.Unix(1, 0)
	ctx := withCheckProgress(t.Context(), 14, func() time.Time { return now }, func(line string) { feeds.Append(id, line) })
	items := make([]check.Item, 14)
	calls := 0
	e := NewEvaluator(nil, nil, nil).WithCheckJudge(progressInvoker(func(_ context.Context, it check.Item, _ modelcall.Call, _ string) CheckResult {
		calls++
		feed, _ := feeds.Read(id, 0)
		want := fmt.Sprintf("%d of 14 · %s (django) · checking", calls, it.Name)
		if len(feed.Lines) != 2*calls-1 || feed.Lines[len(feed.Lines)-1] != want {
			t.Fatalf("start not live: %v", feed.Lines)
		}
		now = now.Add(3200 * time.Millisecond)
		return CheckResult{Verdict: check.Verdict{Verdict: "pass"}, Evidence: check.AgentEvidence{Reused: true}}
	}))
	for i := range items {
		items[i].Declared = check.Declared{Namespace: "django", Declaration: check.Declaration{Name: fmt.Sprintf("rule-%d", i+1)}}
	}
	_, _, err := e.judgeChecks(ctx, modelcall.Call{Model: "test"}, items, "")
	if err != nil {
		t.Fatal(err)
	}
	feed, _ := feeds.Finish(id)
	if len(feed.Lines) != 28 {
		t.Fatalf("expected 28 lines, got %v", feed.Lines)
	}
	for i := range items {
		want := fmt.Sprintf("%d of 14 · rule-%d (django) · reused · 3.2s", i+1, i+1)
		if feed.Lines[2*i+1] != want {
			t.Errorf("finish: %q, want %q", feed.Lines[2*i+1], want)
		}
	}
}

func TestUnknownCheckTotalDoesNotInventOne(t *testing.T) {
	t.Parallel()
	var lines []string
	ctx := withCheckProgress(t.Context(), 0, time.Now, func(line string) { lines = append(lines, line) })
	finish := startCheck(ctx, check.Declared{Declaration: check.Declaration{Name: "rule"}})
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "check 1 · rule") || strings.Contains(lines[0], " of ") {
		t.Fatal(lines)
	}
	_ = finish
}
