package workflowexec

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/tasks"
	"github.com/flokkit/apptown/internal/progress"

	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// An empty turn is re-run once, and both tries are invocations that spent
// tokens: the ledger counts the usage of each, not only the last one's.
func TestEmptyTurnRetryCountsTheUsageOfBothTries(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	dir := t.TempDir()
	var names []string
	a := testExecutor(db, dir, reviewProcess(func(req execution.Request) execution.Outcome {
		names = append(names, req.Name)
		text := ""
		if len(names) == 2 {
			text = `,"content":[{"type":"text","text":"done"}]`
		}
		return execution.Outcome{Output: fmt.Appendf(nil, `{"type":"message_end","message":{"role":"assistant","model":"m","provider":"p"%s,"usage":{"input":%d,"output":0,"totalTokens":%d}}}`+"\n", text, len(names)*1000, len(names)*1000)}
	}))
	res := a.piInvoke(t.Context(), piCall{
		Source:    workflow.Source{ProjectID: idstest.N(1), RunID: idstest.N(5), RootRunID: idstest.N(5), WorkflowName: "w", WorkflowVersion: 1},
		Step:      &workflow.Step{ID: idstest.N(9), Attempt: 1, Name: "implement"},
		Workspace: t.TempDir(), Name: "apptown-step-00000000-0009-7000-8000-000000000000", RetryEmpty: true,
	})
	if res.Tries != 2 || res.Text != "done" || len(names) != 2 || names[1] != "apptown-step-00000000-0009-7000-8000-000000000000-r2" {
		t.Fatalf("an empty turn is re-run once: tries=%d text=%q names=%v", res.Tries, res.Text, names)
	}
	if total, _, err := a.ledger.RunTotal(context.Background(), idstest.N(5)); err != nil || total != 1000+2000 {
		t.Fatalf("both tries count: %d %v", total, err)
	}
}

// An invocation whose transcript holds a broken record records the usage
// that survived, marked incomplete under the invocation's key, and the
// result names the invocation so the step's evidence can say so; a later
// whole try does not make the step's usage read as complete.
func TestInvocationWithABrokenTranscriptRecordsIncompleteUsage(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	dir := t.TempDir()
	calls := 0
	a := testExecutor(db, dir, reviewProcess(func(req execution.Request) execution.Outcome {
		calls++
		out := `{"type":"message_end","message":{"role":"assistant","model":"m","provider":"p","usage":{"input":100,"output":0,"totalTokens":100}}}` + "\n"
		if calls == 1 {
			return execution.Outcome{Output: []byte(out + `{"type":"message_end","message":{"role":"assistant","usage":{"input":90` + "\n")}
		}
		return execution.Outcome{Output: []byte(`{"type":"message_end","message":{"role":"assistant","model":"m","provider":"p","content":[{"type":"text","text":"done"}],"usage":{"input":7,"output":0,"totalTokens":7}}}` + "\n")}
	}))
	res := a.piInvoke(t.Context(), piCall{
		Source:    workflow.Source{ProjectID: idstest.N(1), RunID: idstest.N(5), RootRunID: idstest.N(5), WorkflowName: "w", WorkflowVersion: 1},
		Step:      &workflow.Step{ID: idstest.N(9), Attempt: 2, Name: "implement"},
		Workspace: t.TempDir(), Name: "apptown-step-00000000-0009-7000-8000-000000000000", RetryEmpty: true,
	})
	if res.Tries != 2 || !strings.HasPrefix(res.UsageIncomplete, "apptown-step-00000000-0009-7000-8000-000000000000: ") {
		t.Fatalf("the step's usage names the incomplete invocation: tries=%d %q", res.Tries, res.UsageIncomplete)
	}
	first, found, err := a.ledger.Observed(context.Background(), "step:00000000-0009-7000-8000-000000000000:a2:apptown-step-00000000-0009-7000-8000-000000000000")
	if err != nil || !found || first.Incomplete == "" || first.Totals.Total != 100 {
		t.Fatalf("the broken invocation: %+v %t %v", first, found, err)
	}
	second, found, err := a.ledger.Observed(context.Background(), "step:00000000-0009-7000-8000-000000000000:a2:apptown-step-00000000-0009-7000-8000-000000000000-r2")
	if err != nil || !found || second.Incomplete != "" || second.Totals.Total != 7 {
		t.Fatalf("the whole invocation: %+v %t %v", second, found, err)
	}
}

// pi's result is its events file, never what reached its output: an
// invocation whose output holds a whole answer but whose file is not there
// is an error with unknown usage, never "no output" and never an empty turn
// to re-run; one whose output lost everything still answers from its file.
//
// Kills: the result parsed from the process's output.
func TestAnInvocationIsReadFromItsEventsFileNotItsOutput(t *testing.T) {
	t.Parallel()
	answer := []byte(`{"type":"message_end","message":{"role":"assistant","model":"m","provider":"p","content":[{"type":"text","text":"done"}],"usage":{"input":5,"output":0,"totalTokens":5}}}` + "\n")
	call := func(a *Executor, step int64) piResult {
		return a.piInvoke(t.Context(), piCall{
			Source:    workflow.Source{ProjectID: idstest.N(1), RunID: idstest.N(5), RootRunID: idstest.N(5), WorkflowName: "w", WorkflowVersion: 1},
			Step:      &workflow.Step{ID: idstest.N(9), Attempt: step, Name: "implement"},
			Workspace: t.TempDir(), Name: "apptown-step-00000000-0009-7000-8000-000000000000", RetryEmpty: true,
		})
	}
	// The output was lost; the file holds the answer.
	lost := testExecutor(databaseSuite.New(t), t.TempDir(), reviewProcess(func(req execution.Request) execution.Outcome {
		return execution.Outcome{Termination: execution.Exited, Output: answer}
	}))
	if res := call(lost, 1); res.Text != "done" || res.Tries != 1 || res.RunErr != nil {
		t.Fatalf("from the file: %+v", res)
	}
	// No file: the stand-in answers on the output only.
	tries := 0
	unread := testExecutor(databaseSuite.New(t), t.TempDir(), processFunc(func(req execution.Request) execution.Outcome {
		tries++
		return execution.Outcome{Termination: execution.Exited, Output: answer}
	}))
	res := call(unread, 1)
	if res.Text != "" || tries != 1 || res.RunErr == nil || !strings.Contains(res.RunErr.Error(), "the agent's output could not be read") {
		t.Fatalf("no events file: tries %d, %+v", tries, res)
	}
	if total, _, err := unread.ledger.RunTotal(context.Background(), idstest.N(5)); err != nil || total != 0 {
		t.Fatalf("an unread output recorded usage: %d %v", total, err)
	}
}

// processFunc is a Process that answers req as it is given, with no
// capture: what it prints never reaches an events file.
type processFunc func(execution.Request) execution.Outcome

func (p processFunc) Run(_ context.Context, _ string, req execution.Request) execution.Outcome {
	return p(req)
}

// The worker's capture of an invocation's events is its result; one the
// worker could not read, or that was not there, or was too large, is an
// unread output, never an empty one.
func TestAWorkerInvocationsEventsAreItsCapturedFile(t *testing.T) {
	t.Parallel()
	if got, err := invocationEvents(map[string]execution.ResultFile{"events": {State: execution.ResultPresent, Content: []byte("{}\n")}}, nil); err != nil || string(got) != "{}\n" {
		t.Fatalf("a captured file: %q %v", got, err)
	}
	for name, results := range map[string]map[string]execution.ResultFile{
		"none":      {},
		"absent":    {"events": {State: execution.ResultAbsent}},
		"too large": {"events": {State: execution.ResultTooLarge, Size: 70 << 20}},
		"refused":   {"events": {State: execution.ResultRefused, Reason: execution.RefusedSymlink}},
	} {
		if got, err := invocationEvents(results, nil); err == nil || got != nil {
			t.Fatalf("%s: %q %v", name, got, err)
		}
	}
	if _, err := invocationEvents(nil, fmt.Errorf("the worker did not answer")); err == nil {
		t.Fatal("an unreadable capture was read as events")
	}
}

// Both execution paths must make failed accounting visible and refuse an
// extra model call; a successful model answer does not make the write pass.
func TestAnInvocationWhoseUsageCannotBeRecordedStops(t *testing.T) {
	t.Parallel()
	events := []byte(`{"type":"message_end","message":{"role":"assistant","model":"m","provider":"p","usage":{"input":5,"output":0,"totalTokens":5}}}` + "\n")
	for _, worker := range []bool{false, true} {
		t.Run(fmt.Sprintf("worker=%v", worker), func(t *testing.T) {
			db := databaseSuite.New(t)
			calls := 0
			a := testExecutor(db, t.TempDir(), reviewProcess(func(execution.Request) execution.Outcome {
				calls++
				return execution.Outcome{Termination: execution.Exited, Output: events}
			}))
			source := workflow.Source{ProjectID: idstest.N(1), RunID: idstest.N(5), RootRunID: idstest.N(5), WorkflowName: "w", WorkflowVersion: 1}
			step := &workflow.Step{ID: idstest.N(9), Attempt: 1, Name: "implement"}
			t.Cleanup(db.FailNextStatement("-- name: RecordUsageOnce"))
			var result piResult
			if worker {
				r := invocationResult{piResult: piResult{Events: events}}
				a.recordWorkerUsage(t.Context(), &workerAttempt{source: source, step: step}, tasks.Attempt{Admission: tasks.Admission{Operation: "usage-write-test"}}, agentAdmission{Invocation: agentInvocation}, &r)
				if r.emptyTurn() {
					t.Fatal("failed usage write earned another invocation")
				}
				result = r.piResult
			} else {
				result = a.piInvoke(t.Context(), piCall{Source: source, Step: step, Workspace: t.TempDir(), Name: "usage-write-test", RetryEmpty: true})
				if calls != 1 {
					t.Fatalf("model ran %d times", calls)
				}
			}
			if result.UsageError == nil || result.RunErr == nil || !strings.Contains(result.UsageIncomplete, "recording step usage") {
				t.Fatalf("lost accounting hidden: %+v", result)
			}
			if total, _, err := a.ledger.RunTotal(t.Context(), source.RunID); err != nil || total != 0 {
				t.Fatalf("failed write fabricated usage: %d %v", total, err)
			}
		})
	}
}

func TestAReviewWithUnrecordedUsageCannotPassOrAskAgain(t *testing.T) {
	t.Parallel()
	pass := `{"verdict":"pass","summary":"fine","findings":[]}`
	for _, report := range []string{pass, "", "broken"} {
		for _, followup := range []bool{false, true} {
			if followup && report == pass {
				continue
			}
			for _, timedOut := range []bool{false, true} {
				t.Run(fmt.Sprintf("report=%q/followup=%v/timedOut=%v", report, followup, timedOut), func(t *testing.T) {
					a := &Executor{progress: &progress.Feeds{}}
					path := filepath.Join(t.TempDir(), "review.json")
					if report != "" {
						if err := os.WriteFile(path, []byte(report), 0600); err != nil {
							t.Fatal(err)
						}
					}
					failed := piResult{UsageError: errors.New("recording step usage: unavailable"), UsageIncomplete: "recording step usage: unavailable"}
					outcome := failed
					if followup {
						outcome = piResult{}
					}
					outcome.TimedOut = timedOut
					calls := 0
					evidence := map[string]any{}
					v, failure := a.readReviewVerdict(idstest.N(9), path, "m", outcome, func([]string, time.Duration, string) piResult {
						calls++
						if err := os.WriteFile(path, []byte(pass), 0600); err != nil {
							t.Fatal(err)
						}
						return failed
					}, evidence)
					wantCalls := 0
					if followup {
						wantCalls = 1
					}
					if v.Verdict != "" || !strings.Contains(failure, "recording step usage") || calls != wantCalls || evidence["usageIncomplete"] != failed.UsageIncomplete {
						t.Fatalf("verdict=%+v failure=%q calls=%d evidence=%v", v, failure, calls, evidence)
					}
				})
			}
		}
	}
}
