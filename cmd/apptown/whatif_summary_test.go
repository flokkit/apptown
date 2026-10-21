package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// A what-if's summary on the command line says a rule the version moves
// back as such — never as a check answer with no check — and counts it on
// the project's line, and it says when the what-if moves other workflows.
// Kills: a rule move printed as "older-version · p · " with an empty check;
// the project line silent about moving back; the advance choice unsaid.
func TestAWhatIfSummarySaysWhatMovesBack(t *testing.T) {
	var w whatIf
	if err := json.Unmarshal([]byte(`{"state":"done","projectsTotal":1,"advanceWorkflows":true,
		"rows":[{"projectSlug":"shop","block":"notes","result":"older-version","detail":"notes v2→v1"},
		        {"projectSlug":"shop","block":"workflow implement","result":"newer-version","detail":"workflow implement v40→v52"}],
		"projects":[{"projectSlug":"shop","outcome":"could-not-assess","score":{"movesBack":1}}]}`), &w); err != nil {
		t.Fatal(err)
	}
	got := whatIfSummary(w)
	for _, want := range []string{"shop · moves back: notes v2→v1", "shop · moves forward: workflow implement v40→v52", "moves 1 rule(s) back", "moves each project's other workflows to the newest version"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "older-version · shop") {
		t.Errorf("a rule move printed as a check row:\n%s", got)
	}
}
