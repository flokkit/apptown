package adrs

import "testing"

func TestDecisionLabelsKeepAcceptanceAndRetirementDistinct(t *testing.T) {
	t.Parallel()
	for state, want := range map[State]string{0: "unknown", Draft: "waiting", Proposed: "waiting for you", Accepted: "passed", Rejected: "failed", Deprecated: "stopped", Superseded: "stopped"} {
		if got := state.Display().String(); got != want {
			t.Errorf("%v: %s, want %s", state, got, want)
		}
	}
}
