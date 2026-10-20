package workflowexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/forge/repositoryedit"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/ids"
)

func TestParseReviewVerdict(t *testing.T) {
	t.Parallel()
	cases := []struct {
		raw  string
		ok   bool
		want string
	}{
		{`{"verdict":"pass","summary":"s","findings":[]}`, true, "pass"},
		{`{"verdict":"fail","summary":"s","findings":["a","b"]}`, true, "fail"},
		{`{"verdict":"pa`, false, ""},                    // truncated mid-write
		{`{"verdict":"maybe","summary":"s"}`, false, ""}, // invented verdict
		{`{"summary":"s"}`, false, ""},                   // no verdict at all
		{``, false, ""},
		{`pass`, false, ""}, // not JSON
	}
	for _, c := range cases {
		v, ok := parseReviewVerdict([]byte(c.raw))
		if ok != c.ok || v.Verdict != c.want {
			t.Errorf("%q: got ok=%v verdict=%q, want ok=%v verdict=%q", c.raw, ok, v.Verdict, c.ok, c.want)
		}
	}
	v, _ := parseReviewVerdict([]byte(`{"verdict":"fail","summary":"claims X","findings":["X missing","Y extra"]}`))
	if v.Summary != "claims X" || len(v.Findings) != 2 {
		t.Fatalf("summary/findings must survive parsing: %+v", v)
	}
}

// A second invalid verdict remains an error carrying the raw text; there is
// only one wrap-up invocation, never an unbounded retry of the reviewer. The
// review and its wrap-up are distinct invocations: each one's usage counts.
func TestReviewUnparseableVerdictAfterResumeIsError(t *testing.T) {
	t.Parallel()
	calls := 0
	work, ctx, stepIDs, run, ledger := reviewFixture(t, false, func(req execution.Request) execution.Outcome {
		calls++
		out := execution.Outcome{Output: []byte(`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"working"}]}}` + "\n")}
		if calls == 2 {
			out = reviewFile(t, req, `{"verdict":"maybe","summary":"?"}`)
		}
		out.Output = append(out.Output, fmt.Sprintf(`{"type":"message_end","message":{"role":"assistant","model":"m","provider":"p","usage":{"input":%d,"output":1,"totalTokens":%d}}}`+"\n", calls*100, calls*100+1)...)
		return out
	})
	run("review")
	if calls != 2 {
		t.Fatalf("one resume only, got %d calls", calls)
	}
	got, err := work.Step(ctx, stepIDs["review"])
	if err != nil {
		t.Fatal(err)
	}
	var evidence map[string]any
	if err := json.Unmarshal(got.Step.Evidence, &evidence); err != nil {
		t.Fatal(err)
	}
	if got.Step.Status != workflow.StatusError || evidence["reason"] != "reviewer verdict unparseable" || !strings.Contains(fmt.Sprint(evidence["raw"]), "maybe") {
		t.Fatalf("want error with raw text, got %s %v", got.Step.Status, evidence)
	}
	if rep, err := ledger.Summary(ctx, 1, ids.Nil, ""); err != nil || rep.Totals.Total != 101+201 {
		t.Fatalf("the review and its wrap-up each count: %+v %v", rep.Totals, err)
	}
}

func TestReviewIsNotConsultedWhenTheBootFailed(t *testing.T) {
	t.Parallel()
	for _, boot := range []bool{true, false} {
		t.Run(fmt.Sprintf("boot=%t", boot), func(t *testing.T) {
			t.Parallel()
			calls := 0
			work, ctx, stepIDs, run, _ := reviewFixture(t, boot, func(req execution.Request) execution.Outcome {
				calls++
				if boot {
					t.Error("the reviewer must not run on a tree that did not boot")
				}
				return reviewFile(t, req, `{"verdict":"pass","summary":"fine","findings":[]}`)
			})
			if boot {
				run("boot")
				failed, err := work.Step(ctx, stepIDs["boot"])
				if err != nil || failed.Step.Status != workflow.StatusFail {
					t.Fatalf("boot did not fail: %+v %v", failed, err)
				}
			}
			run("review")
			got, err := work.Step(ctx, stepIDs["review"])
			if err != nil {
				t.Fatal(err)
			}
			if boot {
				if calls != 0 || got.Step.Status != workflow.StatusFail {
					t.Fatalf("reviewer calls=%d, status=%s", calls, got.Step.Status)
				}
				if !strings.Contains(string(got.Step.Evidence), "did not come up") || !strings.Contains(string(got.Step.Evidence), "notRun") {
					t.Fatalf("missing boot refusal evidence: %s", got.Step.Evidence)
				}
			} else if calls == 0 || got.Step.Status != workflow.StatusPass {
				t.Fatalf("without a boot step, review must run and pass: calls=%d, status=%s", calls, got.Step.Status)
			}
		})
	}
}

// The scope guard's refusals are deterministic, so a review whose changed
// files could not be read has not been assessed: the step is an error that
// says so, never an empty scope and never a pass, whether the read fails
// while the brief is written (the reviewer is then not consulted) or when
// the verdict is guarded.
func TestReviewWithAnUnreadableScopeIsAnError(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name       string
		failOnRead int // which read of the changed files fails: 1 brief, 2 guard
		wantCalls  int
	}{
		{"brief", 1, 0},
		{"guard", 2, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			calls, reads := 0, 0
			work, ctx, stepIDs, run, _ := reviewFixtureWith(t, false, func(req execution.Request) execution.Outcome {
				calls++
				return reviewFile(t, req, `{"verdict":"pass","summary":"fine","findings":[]}`)
			}, func(e *Executor) {
				e.changedFiles = func(context.Context, string, string) ([]repositoryedit.ChangedFile, error) {
					reads++
					if reads == c.failOnRead {
						return nil, errors.New("diffing base..HEAD: unknown revision")
					}
					return nil, nil
				}
			})
			run("review")
			got, err := work.Step(ctx, stepIDs["review"])
			if err != nil {
				t.Fatal(err)
			}
			var evidence map[string]any
			if err := json.Unmarshal(got.Step.Evidence, &evidence); err != nil {
				t.Fatal(err)
			}
			reason := fmt.Sprint(evidence["reason"])
			if got.Step.Status != workflow.StatusError || !strings.Contains(reason, "could not be assessed") || !strings.Contains(reason, "unknown revision") {
				t.Fatalf("want an error saying the scope could not be assessed, got %s %v", got.Step.Status, evidence)
			}
			if calls != c.wantCalls {
				t.Fatalf("reviewer calls = %d, want %d", calls, c.wantCalls)
			}
		})
	}
}

// A report the workflow requires under .apptown is collected, never committed
// (publication refuses it), so the reviewer is told its absence from the
// diff is correct; a report elsewhere is committed source and required.
func TestTheReviewerIsToldWhereMandatedReportsLive(t *testing.T) {
	t.Parallel()
	agent := func(file string) *workflow.Step {
		return &workflow.Step{Type: "agent/pi", Config: json.RawMessage(`{"output":{"kind":"change-report","file":"` + file + `"}}`)}
	}
	collected := mandatedOutputsNote([]*workflow.Step{agent(".apptown/CHANGE.md")})
	if !strings.Contains(collected, ".apptown/CHANGE.md") || !strings.Contains(collected, "ABSENT from the diff") || strings.Contains(collected, "Changes to them are REQUIRED") {
		t.Fatalf("a .apptown report must be described as collected and absent from the diff, got:\n%s", collected)
	}
	committed := mandatedOutputsNote([]*workflow.Step{agent("REPORT.md")})
	if !strings.Contains(committed, "REPORT.md") || !strings.Contains(committed, "Changes to them are REQUIRED") || strings.Contains(committed, "ABSENT") {
		t.Fatalf("a committed report must be described as a required change, got:\n%s", committed)
	}
	if note := mandatedOutputsNote([]*workflow.Step{{Type: "agent/review", Config: json.RawMessage(`{"output":{"file":"X.md"}}`)}}); note != "" {
		t.Fatalf("only producing steps mandate reports, got %q", note)
	}
}

// A change whose report was written but not committed (the diff touches
// none of it) passes review when the reviewer passes it: the reviewer is
// shown the collected report as the claim, and no forge rule fails it.
func TestAnUncommittedReportIsShownToTheReviewerAndPasses(t *testing.T) {
	t.Parallel()
	const report = "CHANGED greeting.go: says hello in te reo"
	var prompt string
	work, ctx, stepIDs, run, _ := reviewFixtureSteps(t, []schema.Step{{Name: "review", Uses: ReviewKind, With: map[string]any{"of": "change-report"}}},
		func(req execution.Request) execution.Outcome {
			prompt = strings.Join(req.Argv, "\n")
			return reviewFile(t, req, `{"verdict":"pass","summary":"fine","findings":[]}`)
		}, nil)
	step, err := work.Step(ctx, stepIDs["review"])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := work.AttachArtifact(ctx, step.Step.RunID, workflow.ArtifactDraft{Kind: "change-report", Name: "change-report", Body: report}); err != nil {
		t.Fatal(err)
	}
	run("review")
	got, err := work.Step(ctx, stepIDs["review"])
	if err != nil {
		t.Fatal(err)
	}
	if got.Step.Status != workflow.StatusPass {
		t.Fatalf("an uncommitted report must not fail the review: %s %s", got.Step.Status, got.Step.Evidence)
	}
	if !strings.Contains(prompt, report) {
		t.Fatalf("the reviewer was not shown the report:\n%s", prompt)
	}
}

// The forge's file check is not the reviewer: when it fails a review the
// model passed, the recorded failure is the forge's and the reviewer's own
// summary is kept apart as a pass; when both fail, both are said.
func TestTheForgesFileCheckFailureIsNotPresentedAsTheReviewers(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		verdict, wantReason string
		reviewerSummary     any
	}{
		{`{"verdict":"pass","summary":"says hello as asked","findings":[]}`, "AppTown's file check failed: 1 changed file the report doesn't mention", "says hello as asked"},
		{`{"verdict":"fail","summary":"the greeting is wrong","findings":["wrong word"]}`, "reviewer rejected: the greeting is wrong; AppTown's file check also failed: 1 changed file the report doesn't mention", nil},
	} {
		work, ctx, stepIDs, run, _ := reviewFixtureSteps(t, []schema.Step{{Name: "review", Uses: ReviewKind, With: map[string]any{"of": "change-report"}}},
			func(req execution.Request) execution.Outcome { return reviewFile(t, req, c.verdict) },
			func(e *Executor) {
				e.changedFiles = func(context.Context, string, string) ([]repositoryedit.ChangedFile, error) {
					return []repositoryedit.ChangedFile{{Path: "greeting.go"}, {Path: "Gemfile"}}, nil
				}
			})
		step, err := work.Step(ctx, stepIDs["review"])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := work.AttachArtifact(ctx, step.Step.RunID, workflow.ArtifactDraft{Kind: "change-report", Name: "change-report", Body: "CHANGED greeting.go: says hello"}); err != nil {
			t.Fatal(err)
		}
		run("review")
		got, err := work.Step(ctx, stepIDs["review"])
		if err != nil {
			t.Fatal(err)
		}
		var ev map[string]any
		if err := json.Unmarshal(got.Step.Evidence, &ev); err != nil {
			t.Fatal(err)
		}
		if got.Step.Status != workflow.StatusFail || ev["reason"] != c.wantReason || ev["reviewerSummary"] != c.reviewerSummary {
			t.Fatalf("want reason %q and reviewer summary %v, got %s %v", c.wantReason, c.reviewerSummary, got.Step.Status, ev)
		}
		if c.reviewerSummary != nil && (ev["summary"] != c.wantReason || ev["reviewerVerdict"] != "pass") {
			t.Fatalf("the model's pass must not be the step's summary: %v", ev)
		}
	}
}

// A person's send-back note is part of the request the reviewer checks the
// work against: work it asks for is in scope. A machine verdict is not a
// person's note and is never presented as one.
func TestTheReviewerIsShownThePersonsSendBackNote(t *testing.T) {
	t.Parallel()
	var executor *Executor
	work, ctx, stepIDs, run, _ := reviewFixtureSteps(t, []schema.Step{{Name: "review", Uses: ReviewKind}, {Name: "accept", Uses: "approval", With: map[string]any{"means": "I would ship this"}}},
		func(req execution.Request) execution.Outcome {
			return reviewFile(t, req, `{"verdict":"fail","summary":"the machine's own words","findings":["x"]}`)
		}, func(e *Executor) { executor = e })
	brief := func() reviewBrief {
		t.Helper()
		got, err := work.Step(ctx, stepIDs["review"])
		if err != nil {
			t.Fatal(err)
		}
		r, err := workflow.ReadRunIn(ctx, executor.db, got.Step.RunID)
		if err != nil {
			t.Fatal(err)
		}
		b, err := executor.reviewBrief(ctx, r, workflow.Source{}, reviewConfig{}, "the diff", func(context.Context) (string, []repositoryedit.ChangedFile, error) { return "", nil, nil })
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	if b := brief(); strings.Contains(b.request, "SENT THIS WORK BACK") {
		t.Fatalf("no person has sent the work back, yet the request says one did:\n%s", b.request)
	}
	run("review")
	if b := brief(); strings.Contains(b.request, "SENT THIS WORK BACK") || strings.Contains(b.request, "the machine's own words") {
		t.Fatalf("a reviewer's verdict was presented as a person's note:\n%s", b.request)
	}
	const note = "also upgrade the logging library to 2.x"
	if _, err := work.Approve(ctx, workflow.Approval{StepID: stepIDs["accept"], Decision: workflow.VoteReject, Comment: note}); err != nil {
		t.Fatal(err)
	}
	if b := brief(); !strings.Contains(b.request, "Review the change") || !strings.Contains(b.request, "SENT THIS WORK BACK") || !strings.Contains(b.request, note+" (— reviewer)") {
		t.Fatalf("the person's note is not part of the request:\n%s", b.request)
	}
}

// A review step's own question replaces the shared "does the diff fully
// satisfy the request?" and the verdict rules that follow from it: a
// reproduction is judged on whether it shows the bug, and a reviewer asked
// the shared question would fail an honest "could not reproduce" until the
// reproducer faked one. Without one, the
// shared question stands.
func TestAReviewStepsOwnQuestionReplacesTheSharedOne(t *testing.T) {
	t.Parallel()
	var executor *Executor
	work, ctx, stepIDs, _, _ := reviewFixtureSteps(t, []schema.Step{{Name: "review", Uses: ReviewKind}}, nil, func(e *Executor) { executor = e })
	got, err := work.Step(ctx, stepIDs["review"])
	if err != nil {
		t.Fatal(err)
	}
	r, err := workflow.ReadRunIn(ctx, executor.db, got.Step.RunID)
	if err != nil {
		t.Fatal(err)
	}
	scope := func(context.Context) (string, []repositoryedit.ChangedFile, error) { return "", nil, nil }
	const shared = "Does the diff fully satisfy the request"
	const own = "Is the report an honest account of trying to reproduce the bug in ${{change.title}}?"
	// The shared question accepts an empty diff when the base already meets
	// the request (soak 34: an adoption whose checks already held was failed
	// twice for changing nothing, and the agent invented a server).
	const alreadyMet = "When the\n   base already does what the request asks, an empty diff satisfies it"
	for _, c := range []struct {
		question  string
		want      []string
		wantNever []string
	}{
		{"", []string{"1. " + shared, alreadyMet, "- the request not fully satisfied, or", `verdict must be "fail" if the request is not fully satisfied`}, []string{"question 1"}},
		{own, []string{"1. Is the report an honest account of trying to reproduce the bug in Review the change?", "- the answer to question 1 is no, or", "If the answer to question 1 is yes and", `verdict must be "fail" if the answer to question 1 is no`}, []string{shared, alreadyMet, "fully satisfied", "fully satisfies"}},
	} {
		b, err := executor.reviewBrief(ctx, r, workflow.Source{}, reviewConfig{Question: c.question}, "the diff", scope)
		if err != nil {
			t.Fatal(err)
		}
		prompt := b.prompt()
		for _, w := range c.want {
			if !strings.Contains(prompt, w) {
				t.Errorf("question %q: the prompt lacks %q:\n%s", c.question, w, prompt)
			}
		}
		for _, w := range c.wantNever {
			if strings.Contains(prompt, w) {
				t.Errorf("question %q: the prompt still says %q:\n%s", c.question, w, prompt)
			}
		}
	}
}

// The reviewer is handed the findings the conformance gate could not
// attribute, and is told when the gate's evidence could not be read rather
// than being told nothing.
func TestTheReviewerIsToldTheUndecidedFindingsOrThatTheyAreUnknown(t *testing.T) {
	t.Parallel()
	gate := func(evidence string) []*workflow.Step {
		return []*workflow.Step{{Type: "guidance/conformance", Evidence: json.RawMessage(evidence)}, {Type: "agent/review"}}
	}
	if note := undecidedFindingsNote(gate(`{"undecided":["dep: two lockfiles"]}`)); !strings.Contains(note, "UNDECIDED CONFORMANCE FINDINGS") || !strings.Contains(note, "- dep: two lockfiles") {
		t.Errorf("undecided findings: %q", note)
	}
	if note := undecidedFindingsNote(gate(`{"undecided":"dep: two lockfiles"}`)); !strings.Contains(note, "CONFORMANCE EVIDENCE UNREADABLE") {
		t.Errorf("unreadable evidence: %q", note)
	}
	if note := undecidedFindingsNote(gate(`{"checks":[]}`)); note != "" {
		t.Errorf("nothing undecided: %q", note)
	}
}
