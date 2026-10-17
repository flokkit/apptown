package workflowexec

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/ids/idstest"
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
		Source:   workflow.Source{ProjectID: idstest.N(1), RunID: idstest.N(5), RootRunID: idstest.N(5), WorkflowName: "w", WorkflowVersion: 1},
		Step:     &workflow.Step{ID: idstest.N(9), Attempt: 1, Name: "implement"},
		Executor: execution.Local, Workspace: t.TempDir(), Name: "forge-step-00000000-0009-7000-8000-000000000000", RetryEmpty: true,
	})
	if res.Tries != 2 || res.Text != "done" || len(names) != 2 || names[1] != "forge-step-00000000-0009-7000-8000-000000000000-r2" {
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
		Source:   workflow.Source{ProjectID: idstest.N(1), RunID: idstest.N(5), RootRunID: idstest.N(5), WorkflowName: "w", WorkflowVersion: 1},
		Step:     &workflow.Step{ID: idstest.N(9), Attempt: 2, Name: "implement"},
		Executor: execution.Local, Workspace: t.TempDir(), Name: "forge-step-00000000-0009-7000-8000-000000000000", RetryEmpty: true,
	})
	if res.Tries != 2 || !strings.HasPrefix(res.UsageIncomplete, "forge-step-00000000-0009-7000-8000-000000000000: ") {
		t.Fatalf("the step's usage names the incomplete invocation: tries=%d %q", res.Tries, res.UsageIncomplete)
	}
	first, found, err := a.ledger.Observed(context.Background(), "step:00000000-0009-7000-8000-000000000000:a2:forge-step-00000000-0009-7000-8000-000000000000")
	if err != nil || !found || first.Incomplete == "" || first.Totals.Total != 100 {
		t.Fatalf("the broken invocation: %+v %t %v", first, found, err)
	}
	second, found, err := a.ledger.Observed(context.Background(), "step:00000000-0009-7000-8000-000000000000:a2:forge-step-00000000-0009-7000-8000-000000000000-r2")
	if err != nil || !found || second.Incomplete != "" || second.Totals.Total != 7 {
		t.Fatalf("the whole invocation: %+v %t %v", second, found, err)
	}
}
