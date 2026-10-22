package workflow

import (
	"strings"
	"testing"
)

// An adoption Change's brief says what it was decided on when the caller
// names it (a what-if), on its own line, and says nothing of the kind when
// none was named. Kills: the basis dropped; a basis invented for a Change
// opened without one.
func TestAnUpliftBriefSaysWhatItWasDecidedOn(t *testing.T) {
	t.Parallel()
	in := Uplift{Words: []string{"team v2"}, Acceptance: Acceptance{Checks: []AcceptanceCheck{{Block: "docs", Check: "readme"}}}}
	_, plain := upliftBrief(in)
	if strings.Contains(plain, "what-if") {
		t.Fatalf("a brief with no basis names one:\n%s", plain)
	}
	in.Basis = "Assessed in what-if #12 at 3f9a1c27d0e4: new failures."
	_, body := upliftBrief(in)
	if !strings.Contains(body, "Bring this app onto: team v2.\n\n"+in.Basis+"\n") || !strings.Contains(body, "docs · readme") {
		t.Fatalf("the basis on its own line after what the Change brings:\n%s", body)
	}
}

// An adoption Change's brief gives each check's own expectation beside its
// name, so the agent and its reviewer judge the check rather than its name,
// and says that an empty change is the right result when every check already
// holds (soak 34: a reviewer read a tool's working files as app data and the
// agent, sent back twice, invented a server). A check without an expectation
// is listed by name alone. Kills: the expectation dropped; the empty-change
// paragraph dropped; a dangling ": " after a check with no expectation.
func TestAnUpliftBriefSaysWhatEachCheckExpectsAndThatEmptyIsCorrect(t *testing.T) {
	t.Parallel()
	in := Uplift{Words: []string{"team v2"}, Acceptance: Acceptance{Checks: []AcceptanceCheck{
		{Block: "data/networked-database", Check: "state-behind-needs", Expects: "everything a deployed app must keep is in a declared need"},
		{Block: "docs", Check: "readme"},
	}}}
	_, body := upliftBrief(in)
	for _, want := range []string{
		"  - data/networked-database · state-behind-needs: everything a deployed app must keep is in a declared need\n",
		"  - docs · readme\n",
		"when every check above already holds on the base,\nthe right change is empty",
		"add\nnothing (no needs, parts, endpoints or features)",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the brief lacks %q:\n%s", want, body)
		}
	}
}
