package workflow

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

// The typed facts are written in one place and read by one reader: what is
// written reads back with the runner's own keys beside it, and evidence the
// reader cannot vouch for is an error, never an empty answer.
func TestCheckEvidenceReadsBackWhatWasWrittenAndRefusesWhatItCannotRead(t *testing.T) {
	t.Parallel()
	facts := CheckEvidence{
		Checks:      []CheckReceipt{{Block: "b", Version: 1, Check: "health", Evaluator: "runtime", Verdict: VerdictCannotAssess, ForgeFault: true}},
		CheckErrors: 2,
		Undecided:   []string{"a finding"},
	}
	outcome, err := facts.Rework(map[string]any{"reason": "fix it"})
	if err != nil || !outcome.Rework || outcome.Status != StatusFail {
		t.Fatalf("rework: %+v %v", outcome, err)
	}
	got, err := ReadCheckEvidence(outcome.Evidence)
	if err != nil || !reflect.DeepEqual(got, facts) {
		t.Fatalf("read back %+v %v, want %+v", got, err, facts)
	}
	var own struct{ Reason string }
	if json.Unmarshal(outcome.Evidence, &own) != nil || own.Reason != "fix it" {
		t.Fatalf("the runner's own keys must ride beside the facts: %s", outcome.Evidence)
	}
	if got, err := ReadCheckEvidence(nil); err != nil || !reflect.DeepEqual(got, CheckEvidence{}) {
		t.Fatalf("no evidence has no facts: %+v %v", got, err)
	}

	for name, raw := range map[string]string{
		"not an object":             `["checks"]`,
		"checks of another shape":   `{"checks":{"health":"pass"}}`,
		"an unknown verdict":        `{"checks":[{"check":"health","verdict":"maybe"}]}`,
		"a receipt with no verdict": `{"checks":[{"check":"health"}]}`,
		"check errors as text":      `{"checkErrors":"two"}`,
		"undecided as a count":      `{"undecided":3}`,
		"placement as text":         `{"placement":"worker"}`,
	} {
		if _, err := ReadCheckEvidence(json.RawMessage(raw)); err == nil {
			t.Errorf("%s: read as readable", name)
		}
	}

	for _, key := range checkEvidenceKeys {
		if _, err := (CheckEvidence{}).Outcome(StatusPass, map[string]any{key: 1}); err == nil {
			t.Errorf("the extension wrote %q past the typed facts", key)
		}
	}
	if _, err := (CheckEvidence{Checks: []CheckReceipt{{Check: "health"}}}).Outcome(StatusPass, nil); !errors.Is(err, errCheckVerdict) {
		t.Errorf("a receipt with no verdict was written: %v", err)
	}
}
