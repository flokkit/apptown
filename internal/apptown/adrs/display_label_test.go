package adrs

import (
	"strings"
	"testing"
)

func TestDecisionLabelsKeepAcceptanceAndRetirementDistinct(t *testing.T) {
	t.Parallel()
	for state, want := range map[State]string{0: "unknown", Draft: "waiting", proposed: "waiting for you", Accepted: "passed", rejected: "failed", Deprecated: "stopped", Superseded: "stopped"} {
		if got := state.display().String(); got != want {
			t.Errorf("%v: %s, want %s", state, got, want)
		}
	}
}

// A decision's record explains its word in the same terms: a draft reads
// "waiting" and its help says what it waits for, never a second state word.
func TestADecisionRecordExplainsItsWord(t *testing.T) {
	t.Parallel()
	s, p := fixture(t)
	draft := create(t, s, p, "Keep orders in Postgres")
	if draft.StatusLabel.String() != "waiting" || !strings.Contains(draft.StatusHelp, "waiting") {
		t.Fatalf("draft: %s / %q", draft.StatusLabel, draft.StatusHelp)
	}
	for state := range map[State]bool{Draft: true, proposed: true, Accepted: true, rejected: true, Deprecated: true, Superseded: true} {
		if state.help() == "" {
			t.Errorf("%v has no help", state)
		}
	}
}
