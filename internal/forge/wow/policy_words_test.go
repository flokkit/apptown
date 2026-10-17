package wow

import (
	"strings"
	"testing"
)

func TestPolicyReadingsSpeakParameters(t *testing.T) {
	t.Parallel()
	// The server owns the plain-words layers (policy-v2.md): readings and
	// fixes must actually carry the parameters, or the page shows a rule
	// about nothing.
	with := map[string]any{"uses": "deploy", "requires": "scan"}
	if r := Reading("requireStepBefore", with); !strings.Contains(r, "deploy") || !strings.Contains(r, "scan") {
		t.Fatalf("reading lost its parameters: %s", r)
	}
	if f := Fix("requireStepBefore", with); !strings.Contains(f, "scan") {
		t.Fatalf("fix lost its parameters: %s", f)
	}
	for _, rule := range []string{"requireStepBefore", "requireCapabilityBound", "scanFailOnCeiling"} {
		if Reading(rule, with) == "" || Fix(rule, with) == "" {
			t.Fatalf("rule %s missing a reading or fix", rule)
		}
	}
}
