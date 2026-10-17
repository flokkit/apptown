package workflow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// A step's evidence is recorded with every string masked: nested values and
// lists included. Evidence with nothing to mask is recorded byte for byte as
// given, and so is evidence that is not JSON.
//
// Kills: a script's output that printed a secret recorded as it was.
func TestAStepsEvidenceIsRecordedMasked(t *testing.T) {
	t.Parallel()
	s := &Service{masker: func(context.Context) func(string) string {
		return func(v string) string { return strings.ReplaceAll(v, "s3cret-value", "[secret]") }
	}}
	got := s.masked(t.Context(), json.RawMessage(`{"logTail":"echo s3cret-value","checks":[{"detail":"s3cret-value"}],"exitCode":0}`))
	var v struct {
		LogTail string `json:"logTail"`
		Checks  []struct {
			Detail string `json:"detail"`
		} `json:"checks"`
		ExitCode int `json:"exitCode"`
	}
	if err := json.Unmarshal(got, &v); err != nil || v.LogTail != "echo [secret]" || v.Checks[0].Detail != "[secret]" || strings.Contains(string(got), "s3cret-value") {
		t.Fatalf("masked evidence: %s %v", got, err)
	}
	// A number elsewhere in masked evidence keeps every digit, top level and
	// nested (review CT-C04).
	exact := s.masked(t.Context(), json.RawMessage(`{"n":9007199254740993,"log":"s3cret-value","nested":{"m":9007199254740993}}`))
	if !strings.Contains(string(exact), `"n":9007199254740993`) || !strings.Contains(string(exact), `"m":9007199254740993`) || strings.Contains(string(exact), "s3cret-value") {
		t.Fatalf("masking changed a number: %s", exact)
	}
	same := json.RawMessage(`{"b":1,"a":"<plain>"}`)
	if got := s.masked(t.Context(), same); string(got) != string(same) {
		t.Fatalf("evidence with nothing to mask was rewritten: %s", got)
	}
	if got := s.masked(t.Context(), json.RawMessage(`not json s3cret-value`)); string(got) != `not json s3cret-value` {
		t.Fatalf("non-JSON evidence: %s", got)
	}
}
