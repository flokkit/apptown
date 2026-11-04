package workflowexec

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/repositoryedit"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/ids"
)

// Files and exit 0 cannot make an attempt pass when its model never answered.
// Output tokens across all messages count, including tool calls with no text.
func TestLocalStepsRequireAModelAnswer(t *testing.T) {
	for _, kind := range []string{AgentKind, ReviewKind} {
		for _, row := range []struct {
			name   string
			events []byte
			reason string
		}{
			{"refused", append(piRefuses("first refusal"), piRefuses("openai refused the call (HTTP 429)")...), "OpenAI refused the call: rate limit or out of credit (HTTP 429)"},
			{"zero output", []byte(`{"type":"message_end","message":{"role":"assistant","usage":{"input":10,"output":0}}}` + "\n"), "fake/model returned nothing"},
			{"warm-up only", cacheWarmOutput, "fake/model returned nothing"},
			{"answered", append(piSays("done"), []byte(`{"type":"message_end","message":{"role":"assistant","usage":{"output":0}}}`+"\n")...), ""},
		} {
			t.Run(kind+"/"+row.name, func(t *testing.T) {
				t.Parallel()
				var a *taskAppTown
				var stepID ids.ID
				if kind == AgentKind {
					a, _, stepID = admittedAgentStep(t, `{"prompt":"write","output":{"kind":"report","file":".apptown/report.md"}}`)
				} else {
					a, _, stepID = admittedReviewStep(t)
				}
				installFakePi(a, func(_ int, req execution.Request) execution.Outcome {
					if kind == AgentKind {
						if err := os.WriteFile(filepath.Join(req.Workspace, ".apptown", "report.md"), []byte("completed report"), 0600); err != nil {
							t.Fatal(err)
						}
					} else {
						writeVerdict(t, req.Workspace, `{"verdict":"pass","summary":"fine","findings":[]}`)
					}
					return execution.Outcome{Output: row.events}
				})
				if err := a.work.ExecuteStep(a.ctx, stepID); err != nil {
					t.Fatal(err)
				}
				status, ev := stepStatus(t, a, stepID)
				want := workflow.StatusPass
				if row.reason != "" {
					want = workflow.StatusError
				}
				if status != want || row.reason != "" && ev["reason"] != row.reason {
					t.Fatalf("got %s %v; want %s %q", status, ev, want, row.reason)
				}
			})
		}
	}
}

func TestWorkerReviewRequiresAModelAnswer(t *testing.T) {
	t.Parallel()
	for _, row := range []struct {
		name   string
		events []byte
		reason string
	}{
		{"refused", piRefuses("openai refused the call (HTTP 429)"), "OpenAI refused the call: rate limit or out of credit (HTTP 429)"},
		{"zero output", nil, "The model returned nothing"},
		{"warm-up only", cacheWarmOutput, "The model returned nothing"},
		{"answered", piSays("reviewed"), ""},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			res := invocationResult{piResult: piResult{Events: row.events, OutputTokens: sumPiUsage(row.events).AssistantOutput}, results: map[string]execution.ResultFile{"review": {State: execution.ResultPresent, Content: []byte(`{"verdict":"pass","summary":"fine","findings":[]}`)}}}
			a := &Executor{}
			j := a.judgeReview(t.Context(), &workerAttempt{run: &workflow.Run{}}, agentAdmission{Invocation: reviewInvocation, Review: &reviewAdmission{}}, res, "", repositoryedit.Candidate{})
			if j.failure != row.reason || row.reason == "" && j.verdict.Verdict != "pass" || row.reason != "" && j.verdict.Verdict != "" {
				t.Fatalf("verdict %+v failure %q fields %v", j.verdict, j.failure, j.fields)
			}
		})
	}
}

// A wrap-up with no output does not erase the initial review's model answer.
func TestModelAnswerSurvivesReviewFollowUp(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		t.Run(fmt.Sprint(invalid), func(t *testing.T) {
			t.Parallel()
			a, _, stepID := admittedReviewStep(t)
			fake := installFakePi(a, func(n int, req execution.Request) execution.Outcome {
				if n == 1 {
					if invalid {
						writeVerdict(t, req.Workspace, `{"verdict":"maybe"}`)
					}
					return execution.Outcome{Output: piSays("reviewed the work")}
				}
				writeVerdict(t, req.Workspace, `{"verdict":"pass","summary":"fine","findings":[]}`)
				return execution.Outcome{Output: []byte(`{"type":"agent_end"}` + "\n")}
			})
			if err := a.work.ExecuteStep(a.ctx, stepID); err != nil {
				t.Fatal(err)
			}
			status, ev := stepStatus(t, a, stepID)
			if status != workflow.StatusPass || len(fake.requests()) != 2 {
				t.Fatalf("got %s %v (%d calls)", status, ev, len(fake.requests()))
			}
		})
	}
}

var cacheWarmOutput = []byte(`{"type":"entry_appended","entry":{"type":"usage","kind":"cache_warm","provider":"p","model":"warm","usage":{"input":2,"output":1,"totalTokens":3,"cost":{"total":0.001}}}}` + "\n")

func TestCacheWarmOutputIsSpendWithoutAModelAnswer(t *testing.T) {
	t.Parallel()
	spent := sumPiUsage(cacheWarmOutput)
	if spent.Totals.Output != 1 || spent.Totals.Total != 3 || spent.Totals.CostMicros != 1000 {
		t.Fatalf("warm-up spend lost: %+v", spent)
	}
	if failure := modelAnswerFailure(spent.AssistantOutput, cacheWarmOutput, "fake/model"); failure != "fake/model returned nothing" {
		t.Fatalf("warm-up counted as answer: %q", failure)
	}
}
