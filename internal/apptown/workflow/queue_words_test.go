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
		{"waiting", "waiting for a person", 0, "approval"},
		{"stuck", "stuck since 2 Jan", 1, ""},
		{"stopped", "stopped after 3 failed tries", 3, ""},
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
