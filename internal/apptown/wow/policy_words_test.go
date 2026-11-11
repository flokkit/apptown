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
	// Both consequences stay: a breaking save is refused, and workflows
	// saved before the policy are warned when they run.
	if r := Reading("requireStepBefore", with); !strings.Contains(r, "refuses a save that breaks it.") || !strings.Contains(r, "Runs of workflows saved before the policy get a warning.") {
		t.Fatalf("reading lost a consequence: %s", r)
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

// A rule's reading and fix are shown to people as sentences: each starts
// with a capital, ends with a full stop and has no semicolon chains.
func TestPolicyReadingsAndFixesAreSentences(t *testing.T) {
	t.Parallel()
	with := map[string]any{"uses": "build", "requires": "scan", "capability": "errors", "max": "high"}
	for _, rule := range []string{"requireStepBefore", "requireCapabilityBound", "scanFailOnCeiling"} {
		for kind, words := range map[string]string{"reading": Reading(rule, with), "fix": Fix(rule, with)} {
			first := words[:1]
			if first != strings.ToUpper(first) || !strings.HasSuffix(words, ".") || strings.Contains(words, ";") {
				t.Errorf("%s %s is not a sentence: %q", rule, kind, words)
			}
		}
	}
}
