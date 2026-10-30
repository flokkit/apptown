package jobs

import "testing"

func TestQueueLabelsKeepProgressSeparateFromRuntime(t *testing.T) {
	for state, want := range map[string]string{"available": "waiting", "retryable": "waiting", "scheduled": "waiting", "pending": "waiting", "running": "working", "completed": "finished", "discarded": "failed", "cancelled": "stopped", "future": "unknown"} {
		if got := stateLabel(state).String(); got != want {
			t.Errorf("%s: %s, want %s", state, got, want)
		}
	}
}
