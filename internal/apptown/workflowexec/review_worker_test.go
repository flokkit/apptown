package workflowexec

import (
	"errors"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/execution"
)

// The verdict table (plan, "Review steps"): a verdict counts only from an
// invocation that exited 0, with no provider error and its report present
// and valid. The review may earn one wrap-up (timed out with its stop
// confirmed, or no report) or one fixup (an invalid report); the wrap-up
// and fixup follow the same rules and never earn another. Every row, for
// every invocation of a review.
func TestTheReviewVerdictTable(t *testing.T) {
	pass := execution.ResultFile{State: execution.ResultPresent, Content: []byte(`{"verdict":"pass","summary":"fine","findings":[]}`)}
	invalid := execution.ResultFile{State: execution.ResultPresent, Content: []byte(`{"verdict":"maybe"`)}
	absent := execution.ResultFile{State: execution.ResultAbsent}
	result := func(code int, report execution.ResultFile, adjust ...func(*invocationResult)) invocationResult {
		r := invocationResult{results: map[string]execution.ResultFile{"review": report}}
		r.Outcome = execution.Outcome{Termination: execution.Exited, ExitCode: code}
		for _, a := range adjust {
			a(&r)
		}
		return r
	}
	timedOut := func(r *invocationResult) {
		r.Outcome = execution.Outcome{Termination: execution.TimedOut, TimedOut: true, ExitCode: -1}
	}
	rows := []struct {
		name     string
		res      invocationResult
		verdict  string // on the review; its follow-ups give the same unless noted
		next     string // the review's next invocation
		failure  string // why no verdict counts, on the review
		followUp string // why no verdict counts, on a wrap-up or fixup ("" = as failure)
	}{
		{name: "exit 0, a valid report", res: result(0, pass), verdict: "pass"},
		{name: "exit 0, no report", res: result(0, absent), next: reviewWrapUpInvocation, followUp: "no .apptown/review.json verdict"},
		{name: "exit 0, an invalid report", res: result(0, invalid), next: reviewFixUpInvocation, followUp: "unparseable"},
		{name: "exit 0, a refused report", res: result(0, execution.ResultFile{State: execution.ResultRefused, Reason: execution.RefusedSymlink}), failure: "refused to read it (symlink)"},
		{name: "exit 0, a report too large", res: result(0, execution.ResultFile{State: execution.ResultTooLarge, Size: 300 << 10}), failure: "over the 256 KiB"},
		{name: "exit 0, no result for the report", res: invocationResult{results: map[string]execution.ResultFile{}}, failure: "returned no result"},
		{name: "exit 0, result files unreadable", res: result(0, pass, func(r *invocationResult) { r.resultsErr = errors.New("worker gone") }), failure: "worker gone"},
		{name: "timed out, a report written", res: result(0, pass, timedOut), next: reviewWrapUpInvocation, followUp: "timed out"},
		{name: "non-zero exit, a pass", res: result(1, pass), failure: "exited with code 1"},
		{name: "a provider error, a pass", res: result(0, pass, func(r *invocationResult) { r.ProviderError = "credit balance too low" }), failure: "provider refused the request: credit balance too low"},
		{name: "output unread, a pass", res: result(0, pass, func(r *invocationResult) { r.outputErr = errors.New("no answer") }), failure: "output could not be read"},
	}
	for _, row := range rows {
		for _, invocation := range []string{reviewInvocation, reviewWrapUpInvocation, reviewFixUpInvocation} {
			v, next, failure := reviewStep(invocation, row.res)
			wantNext, wantFailure := row.next, row.failure
			if invocation != reviewInvocation {
				wantNext = ""
				if row.followUp != "" {
					wantFailure = row.followUp
				}
			}
			if next != wantNext || (wantFailure == "") != (failure == "") || !strings.Contains(failure, wantFailure) {
				t.Errorf("%s, %s: next %q, failure %q", row.name, invocation, next, failure)
			}
			if counted := next == "" && failure == ""; counted && (row.verdict == "" || v.Verdict != row.verdict) {
				t.Errorf("%s, %s: verdict %+v", row.name, invocation, v)
			}
			if (next != "" || failure != "") && v.Verdict != "" {
				t.Errorf("%s, %s: a verdict %q counted beside %q %q", row.name, invocation, v.Verdict, next, failure)
			}
			if invocation == reviewInvocation && nextInvocation(invocation, row.res) != next {
				t.Errorf("%s: the sequence disagrees with the table", row.name)
			}
		}
	}
	// An agent step's sequence is its own: one retry after an empty turn.
	empty := invocationResult{}
	if nextInvocation(agentInvocation, empty) != agentRetryInvocation || nextInvocation(agentRetryInvocation, empty) != "" {
		t.Error("the agent's sequence changed")
	}
}
