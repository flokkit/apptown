package adrs

import "testing"

func TestDecisionLabelsKeepAcceptanceAndRetirementDistinct(t *testing.T) {
	t.Parallel()
	for state, want := range map[State]string{0: "unknown", Draft: "waiting", proposed: "waiting for you", Accepted: "passed", rejected: "failed", Deprecated: "stopped", Superseded: "stopped"} {
		if got := state.display().String(); got != want {
			t.Errorf("%v: %s, want %s", state, got, want)
		}
	}
}
