package workflow

import (
	"testing"

	"github.com/flokkit/apptown/internal/access"
)

func TestPromotedCheckFailureRequiresDecision(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   StepState
		verdict  CheckVerdict
		attempts int
	}{
		{"faulted", StatusError, VerdictError, 3},
		{"held", StatusFail, VerdictFail, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, ch, _, _, _ := automatedFixture(t)
			run := admitAutomatedRun(t, s, ch, "routine", "tests", "approval")
			steps, err := s.RunSteps(t.Context(), run.ID)
			if err != nil {
				t.Fatal(err)
			}
			var routine, tests *Step
			for _, step := range steps {
				if step.Type == "routine" {
					routine = step
				} else if step.Type == "tests" {
					tests = step
				}
			}
			claim, err := newStepAttempts(s.db, func(string) bool { return true }).start(t.Context(), routine, "routine", ch.ProjectID)
			if err != nil {
				t.Fatal(err)
			}
			e := &Execution{service: s, ctx: t.Context(), step: routine, run: run, claim: claim}
			if err := e.StartFix(nil); err != nil {
				t.Fatal(err)
			}
			outcome, err := EncodeOutcome(StatusFail, map[string]string{"reason": "Dockerfile restructure"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := (reworkStore{db: s.db}).fail(t.Context(), claim, outcome); err != nil {
				t.Fatal(err)
			}
			before, err := s.QueueState(t.Context(), ch.ProjectID, "security")
			if err != nil || before.State != "decide" || before.Decide == nil || before.Decide.Act != nextOverride || before.Decide.StepID != routine.ID || before.Words != "Dockerfile restructure" {
				t.Fatalf("held: %+v %v", before, err)
			}
			ctx := access.WithCaller(t.Context(), access.Caller{Verified: true, Principal: access.Principal{Kind: "user", Name: "verified-person", Via: "session"}})
			if _, err := s.Override(ctx, Override{StepID: routine.ID, Reason: "Keep the Dockerfile restructure"}); err != nil {
				t.Fatal(err)
			}
			s.Wait()
			// Exhaust any in-run retries before reading the person's decision.
			for attempt := 1; attempt <= tc.attempts; attempt++ {
				steps, err := s.RunSteps(ctx, run.ID)
				if err != nil {
					t.Fatal(err)
				}
				for _, step := range steps {
					if step.ID == tests.ID {
						tests = step
					}
				}
				testClaim, err := newStepAttempts(s.db, func(string) bool { return true }).start(ctx, tests, "tests", ch.ProjectID)
				if err != nil {
					t.Fatal(err)
				}
				fault, err := (CheckEvidence{Checks: []CheckReceipt{{Check: "tests", Verdict: tc.verdict, AppTownFault: tc.status == StatusError}}}).Outcome(tc.status, map[string]any{"reason": "executor unavailable"})
				if err != nil {
					t.Fatal(err)
				}
				result, err := (reworkStore{db: s.db}).fail(ctx, testClaim, fault)
				if err != nil {
					t.Fatal(err)
				}
				if result.retried != (attempt < tc.attempts) {
					t.Fatalf("attempt %d: %+v", attempt, result)
				}
			}
			got, err := s.QueueState(ctx, ch.ProjectID, "security")
			if err != nil {
				t.Fatal(err)
			}
			if got.State != "decide" || got.ChangeID != ch.ID || got.Failed != 0 || got.Decide == nil || got.Decide.Act != nextOverride || got.Decide.StepID != tests.ID || got.Words != "Executor unavailable" {
				t.Fatalf("open promoted Change with a faulted tests step must retain its check decision: %+v", got)
			}
		})
	}
}
