package workflow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/ids"
)

// Every check uses the engine rule, including assertions, and its actual
// evidence reaches the continuing agent without a runner-specific decoder.
func TestHandoverFailuresReturnTheirEvidenceToTheAgent(t *testing.T) {
	for _, kind := range []string{"tests", "scan", "preview", "artifact/require", "guidance/conformance"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			check := schema.Step{Name: "check-work", Uses: kind}
			if kind == "artifact/require" {
				check.With = map[string]any{"kind": "missing-report"}
			}
			q, run := admitWorkflowSpec(t, schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "finish", OnEnter: []schema.Step{{Name: "build", Uses: "agent/pi"}}, Steps: []schema.Step{check}}}})
			svc := newServiceWithAuthority(q.db, Dependencies{Automatic: []string{"agent/pi", kind}}, allowProject(run.ProjectID))
			svc.dispatcher.stop()
			agent := admittedStepID(t, q, run, "build")
			finishForTest(t, svc, agent, StatusPass)
			id := admittedStepID(t, q, run, "check-work")
			want := "diagnostic at the end"
			if kind == "artifact/require" {
				if _, err := svc.assertions.Evaluate(ctx, Assertion{StepID: id}); err != nil {
					t.Fatal(err)
				}
				want = "missing-report"
			} else {
				step, err := readStep(ctx, q.db, id)
				if err != nil {
					t.Fatal(err)
				}
				claim, err := svc.stepAttempts.start(ctx, step, kind, ids.Nil)
				if err != nil {
					t.Fatal(err)
				}
				evidence := json.RawMessage(`{"reason":"check failed","findings":{"failing":1},"logTail":"diagnostic at the end"}`)
				if err := svc.finishAttempt(ctx, claim, StepOutcome{Status: StatusFail, Evidence: evidence}); err != nil {
					t.Fatal(err)
				}
			}
			if got := storedStep(t, q.db, agent); got.Status != StatusPending {
				t.Fatalf("%s did not return work to agent: %s", kind, got.Status)
			}
			prompt := Interpolate(ctx, q.db, "Repair the work: ${{feedback.latest}}", run, nil, "sample")
			if !strings.Contains(prompt, want) || !strings.Contains(prompt, "check-work") || strings.Contains(prompt, "boot gate") {
				t.Fatalf("wrong prompt: %s", prompt)
			}
			wantEventCount(t, q.db, 1, ofKind("step.rework"))
		})
	}
}

func TestHandoverPersonAndErrorDoNotSpendRounds(t *testing.T) {
	for _, status := range []StepState{StatusNeedsHuman, StatusError} {
		t.Run(status.String(), func(t *testing.T) {
			owner, run, agent, review, _ := retryStageFixture(t)
			svc := newServiceWithAuthority(owner.db, Dependencies{Automatic: []string{"agent/pi", "agent/review"}}, allowProject(run.ProjectID))
			svc.dispatcher.stop()
			claim := reworkReviewClaim(t, svc.db, review)
			if err := svc.finishAttempt(context.Background(), claim, StepOutcome{Status: status, Evidence: json.RawMessage(`{"reason":"bind WP_ADMIN_PASSWORD in Settings > Secrets","actions":["bind WP_ADMIN_PASSWORD in Settings > Secrets"]}`)}); err != nil {
				t.Fatal(err)
			}
			if storedStep(t, svc.db, agent).Status != StatusPass {
				t.Fatal("person/error started agent")
			}
			wantEventCount(t, svc.db, 0, ofKind("step.rework"))
			_ = run
		})
	}
}

func TestHandoverKeepsEachReasonAndStopsAfterThreeRounds(t *testing.T) {
	owner, run, agent, review, _ := retryStageFixture(t)
	svc := newServiceWithAuthority(owner.db, Dependencies{Automatic: []string{"agent/pi", "agent/review"}}, allowProject(run.ProjectID))
	svc.dispatcher.stop()
	for i, reason := range []string{"first failure", "second failure", "third failure", "fourth failure"} {
		if i > 0 {
			finishForTest(t, svc, agent, StatusPass)
		}
		claim := reworkReviewClaim(t, svc.db, review)
		evidence, _ := json.Marshal(map[string]string{"reason": reason})
		if err := svc.finishAttempt(context.Background(), claim, StepOutcome{Status: StatusFail, Evidence: evidence}); err != nil {
			t.Fatal(err)
		}
		want := StatusPending
		if i == 3 {
			want = StatusPass
		}
		if storedStep(t, svc.db, agent).Status != want {
			t.Fatalf("failure %d: wrong agent state", i+1)
		}
	}
	for _, reason := range []string{"first failure", "second failure", "third failure"} {
		wantEventCount(t, svc.db, 1, kindWith("step.rework", "reason", reason))
	}
	wantEventCount(t, svc.db, 3, ofKind("step.rework"))
}

func TestAStillMissingReportGetsEachRoundOfHandoverChecks(t *testing.T) {
	ctx := context.Background()
	q, run := admitWorkflowSpec(t, schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "finish", OnEnter: []schema.Step{{Name: "build", Uses: "agent/pi"}}, Steps: []schema.Step{{Name: "report", Uses: "artifact/require", With: map[string]any{"kind": "change-report"}}, {Name: "accept", Uses: "approval"}}}}})
	svc := newServiceWithAuthority(q.db, Dependencies{Automatic: []string{"agent/pi"}}, allowProject(run.ProjectID))
	svc.dispatcher.stop()
	agent := admittedStepID(t, q, run, "build")
	for i := 0; i < 4; i++ {
		finishForTest(t, svc, agent, StatusPass)
		if err := svc.refreshAssertions(ctx, RunRef{ID: run.ID, ProjectID: run.ProjectID}); err != nil {
			t.Fatal(err)
		}
		want := StatusPending
		if i == 3 {
			want = StatusPass
		}
		if storedStep(t, q.db, agent).Status != want {
			t.Fatalf("round %d did not follow the shared rule", i+1)
		}
	}
	wantEventCount(t, q.db, 3, ofKind("step.rework"))
}

func TestHandoverFeedbackStaysWithTheCheckThatRequestedTheRevision(t *testing.T) {
	ctx := context.Background()
	q, run := admitWorkflowSpec(t, schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "finish", OnEnter: []schema.Step{{Name: "build", Uses: "agent/pi"}}, Steps: []schema.Step{{Name: "scan", Uses: "scan"}, {Name: "tests", Uses: "tests"}}}}})
	svc := newServiceWithAuthority(q.db, Dependencies{Automatic: []string{"agent/pi", "scan", "tests"}}, allowProject(run.ProjectID))
	svc.dispatcher.stop()
	finishForTest(t, svc, admittedStepID(t, q, run, "build"), StatusPass)
	claims := []*Step{}
	for _, kind := range []string{"scan", "tests"} {
		step, err := readStep(ctx, q.db, admittedStepID(t, q, run, kind))
		if err != nil {
			t.Fatal(err)
		}
		claim, err := svc.stepAttempts.start(ctx, step, kind, ids.Nil)
		if err != nil {
			t.Fatal(err)
		}
		claims = append(claims, claim)
	}
	for i, claim := range claims {
		evidence, _ := json.Marshal(map[string]string{"reason": []string{"repair this scan finding", "later unrelated failure"}[i]})
		if err := svc.finishAttempt(ctx, claim, StepOutcome{Status: StatusFail, Evidence: evidence}); err != nil {
			t.Fatal(err)
		}
	}
	prompt := Interpolate(ctx, q.db, "${{feedback.latest}}", run, nil, "sample")
	if !strings.Contains(prompt, "repair this scan finding") || strings.Contains(prompt, "later unrelated failure") {
		t.Fatalf("lost rework cause: %s", prompt)
	}
	wantEventCount(t, q.db, 1, ofKind("step.rework"))
}

func TestHandoverRechecksAnyFollowingCheckAndKeepsEntryPrerequisites(t *testing.T) {
	ctx := context.Background()
	q, run := admitWorkflowSpec(t, schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "finish", OnEnter: []schema.Step{{Name: "prepare", Uses: "script"}, {Name: "build", Uses: "agent/pi"}, {Name: "check", Uses: "script"}}, Steps: []schema.Step{{Name: "accept", Uses: "approval"}}}}})
	svc := newServiceWithAuthority(q.db, Dependencies{Automatic: []string{"agent/pi", "script"}}, allowProject(run.ProjectID))
	svc.dispatcher.stop()
	prep, agent, check := admittedStepID(t, q, run, "prepare"), admittedStepID(t, q, run, "build"), admittedStepID(t, q, run, "check")
	finishForTest(t, svc, prep, StatusPass)
	finishForTest(t, svc, agent, StatusPass)
	step, err := readStep(ctx, q.db, check)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := svc.stepAttempts.start(ctx, step, "script", ids.Nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.finishAttempt(ctx, claim, StepOutcome{Status: StatusFail, Evidence: json.RawMessage(`{"reason":"script check failed"}`)}); err != nil {
		t.Fatal(err)
	}
	if storedStep(t, q.db, agent).Status != StatusPending {
		t.Fatal("script did not request revision")
	}
	finishForTest(t, svc, agent, StatusPass)
	if err := svc.refreshAssertions(ctx, RunRef{ID: run.ID, ProjectID: run.ProjectID}); err != nil {
		t.Fatal(err)
	}
	if storedStep(t, q.db, prep).Status != StatusPass {
		t.Fatal("revision reran an entry prerequisite")
	}
	if storedStep(t, q.db, check).Status != StatusPending {
		t.Fatal("revision failed to rerun the script check")
	}
}

func TestHandoverKeepsFeedbackWhenTheFailingAgentItselfIsRevising(t *testing.T) {
	ctx := context.Background()
	q, run := admitWorkflowSpec(t, schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "finish", OnEnter: []schema.Step{{Name: "build", Uses: "agent/pi"}}, Steps: []schema.Step{{Name: "accept", Uses: "approval"}}}}})
	svc := newServiceWithAuthority(q.db, Dependencies{Automatic: []string{"agent/pi"}}, allowProject(run.ProjectID))
	svc.dispatcher.stop()
	agent, err := readStep(ctx, q.db, admittedStepID(t, q, run, "build"))
	if err != nil {
		t.Fatal(err)
	}
	claim, err := svc.stepAttempts.start(ctx, agent, "agent/pi", ids.Nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.finishAttempt(ctx, claim, StepOutcome{Status: StatusFail, Evidence: json.RawMessage(`{"reason":"the declared report is missing","stderrTail":"write CHANGE.md"}`)}); err != nil {
		t.Fatal(err)
	}
	if storedStep(t, q.db, agent.ID).Status != StatusPending {
		t.Fatal("agent was not asked to revise")
	}
	prompt := Interpolate(ctx, q.db, "${{feedback.latest}}", run, nil, "sample")
	if !strings.Contains(prompt, "the declared report is missing") || !strings.Contains(prompt, "write CHANGE.md") || strings.Contains(prompt, "Check build (agent/pi) failed") {
		t.Fatalf("agent lost the error when its step was reset: %s", prompt)
	}
}

// A missing secret is a person action, not a missing report or an agent failure.
func TestHeldBootKeepsReportPendingWithoutSpendingRework(t *testing.T) {
	for _, status := range []StepState{StatusNeedsHuman, StatusError} {
		t.Run(status.String(), func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			q, run := admitWorkflowSpec(t, schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "finish", OnEnter: []schema.Step{{Name: "build", Uses: "agent/pi"}, {Name: "boot", Uses: "preview"}}, Steps: []schema.Step{{Name: "report", Uses: "artifact/require", With: map[string]any{"kind": "change-report"}}}}}})
			svc := newServiceWithAuthority(q.db, Dependencies{Automatic: []string{"agent/pi", "preview"}}, allowProject(run.ProjectID))
			svc.dispatcher.stop()
			agent, boot, report := admittedStepID(t, q, run, "build"), admittedStepID(t, q, run, "boot"), admittedStepID(t, q, run, "report")
			finishForTest(t, svc, agent, StatusPass)
			step := storedStep(t, q.db, boot)
			claim, err := svc.stepAttempts.start(ctx, step, "preview", ids.Nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := svc.finishAttempt(ctx, claim, StepOutcome{Status: status, Evidence: json.RawMessage(`{"reason":"bind WP_ADMIN_PASSWORD in Settings > Secrets"}`)}); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.assertions.Evaluate(ctx, Assertion{StepID: report}); err != nil {
				t.Fatal(err)
			}
			if got := storedStep(t, q.db, report); got.Status != StatusPending {
				t.Fatalf("report ran during boot hold: %s", got.Status)
			}
			wantEventCount(t, q.db, 0, ofKind("step.rework"))
			if _, err := svc.Override(ctx, Override{StepID: boot, Reason: "secret bound and boot checked"}); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.assertions.Evaluate(ctx, Assertion{StepID: report}); err != nil {
				t.Fatal(err)
			}
			if got := storedStep(t, q.db, report); got.Status != StatusFail {
				t.Fatalf("report did not run after hold settled: %s", got.Status)
			}
			wantEventCount(t, q.db, 1, ofKind("step.rework"))
		})
	}
}

func TestFeedbackRenderingDependsOnEvidenceRatherThanStepType(t *testing.T) {
	t.Parallel()
	for _, evidence := range []string{`{"reason":"previous attempt failed","stderrTail":"write CHANGE.md"}`, `{"comment":"fix the report","approver":"sam"}`} {
		step := Step{Name: "build", Type: "agent/pi", Evidence: json.RawMessage(evidence)}
		want := strings.TrimPrefix(renderFeedback(&step), "Feedback from build (agent/pi):\n")
		for _, kind := range []string{"tests", "agent/review", "approval"} {
			step.Type = kind
			if got := strings.TrimPrefix(renderFeedback(&step), "Feedback from build ("+kind+"):\n"); got != want {
				t.Fatalf("%s changed feedback: %q, want %q", kind, got, want)
			}
		}
	}
}
