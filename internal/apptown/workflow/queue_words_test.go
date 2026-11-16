package workflow

import (
	"testing"
	"time"
)

// Every sentence is read through QueueState after the owners record the work.
func TestQueueStateReturnsWords(t *testing.T) {
	for _, tc := range []struct {
		state, words string
		failures     int
		kind         string
	}{
		{"up_to_date", "up to date", 0, ""},
		{"updating", "updating", 0, "script"},
		{"decide", "needs your yes", 0, "approval"},
		{"decide", "needs you", 1, ""},
		{"stopped", "AppTown could not finish this update", 3, ""},
	} {
		t.Run(tc.state, func(t *testing.T) {
			s, ch, _, _, _ := automatedFixture(t)
			for i := 0; i < tc.failures; i++ {
				janitorTry(t, s, ch.ProjectID, StandingRevision{}, time.Date(2026, 1, 2, 23, 0, 0, 0, time.UTC), true)
			}
			queue := "security"
			if tc.failures > 0 {
				queue = "updates"
			}
			if tc.kind != "" {
				run := admitAutomatedRun(t, s, ch, tc.kind)
				if tc.kind == "script" {
					steps, err := s.RunSteps(t.Context(), run.ID)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := newStepAttempts(s.db, func(string) bool { return true }).start(t.Context(), steps[0], steps[0].Type, ch.ProjectID); err != nil {
						t.Fatal(err)
					}
				}
			}
			got, err := s.QueueState(t.Context(), ch.ProjectID, queue)
			if err != nil || got.State != tc.state || got.Words != tc.words {
				t.Fatalf("queue=%+v: %v", got, err)
			}
		})
	}
}

// The holding step determines the sentence, rather than its technical evidence.
func TestQueueDecisionWordsNameTheFailedCheck(t *testing.T) {
	for _, tc := range []struct{ kind, words string }{
		{"routine", "goes beyond what your settings allow"},
		{"tests", "the app's tests fail with it"},
		{"preview", "the app does not start with it"},
		{"scan", "a security scan found problems in it"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			s, ch, _, _, _ := automatedFixture(t)
			run := admitAutomatedRun(t, s, ch, tc.kind)
			steps, err := s.RunSteps(t.Context(), run.ID)
			if err != nil {
				t.Fatal(err)
			}
			claim, err := newStepAttempts(s.db, func(string) bool { return true }).start(t.Context(), steps[0], tc.kind, ch.ProjectID)
			if err != nil {
				t.Fatal(err)
			}
			e := &Execution{service: s, ctx: t.Context(), step: steps[0], run: run, claim: claim}
			if err := e.StartFix(nil); err != nil {
				t.Fatal(err)
			}
			if _, err := (reworkStore{db: s.db}).fail(t.Context(), claim, StepOutcome{Status: StatusFail}); err != nil {
				t.Fatal(err)
			}
			got, err := s.QueueState(t.Context(), ch.ProjectID, "security")
			if err != nil || got.State != "decide" || got.Words != tc.words || got.Decide == nil || got.Decide.StepID != steps[0].ID || got.Decide.Act != nextOverride {
				t.Fatalf("queue=%+v: %v", got, err)
			}
		})
	}
}
