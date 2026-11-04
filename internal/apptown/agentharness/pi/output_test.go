package pi

import (
	"os"
	"strings"
	"testing"
)

func TestLastModelErrorReadsTheProvidersWords(t *testing.T) {
	t.Parallel()
	out := `{"type":"turn_start"}
{"type":"turn_end","message":{"role":"assistant","content":[],"stopReason":"error","errorMessage":"400 {\"type\":\"error\",\"error\":{\"type\":\"invalid_request_error\",\"message\":\"Your credit balance is too low to access the Anthropic API.\"}}"}}
{"type":"agent_settled"}`
	got := LastModelError([]byte(out))
	if !strings.Contains(got, "credit balance is too low") || !strings.HasPrefix(got, "400 ") {
		t.Fatalf("want the provider's sentence, got %q", got)
	}
	if LastModelError([]byte(`{"type":"turn_end","message":{"stopReason":"stop"}}`)) != "" {
		t.Fatal("a normal turn must not read as an error")
	}
}

// Completed events remain authoritative when update events contain only deltas.
// This stream comes from two real CLI invocations sharing one session.
func TestRecordedPiOutputAndUsage(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("testdata/events.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	text, model, provider, u := ParseOutput(raw)
	if text != "The fixture completed." || model != "pi-upgrade" || provider != "fixture" || u["totalTokens"] != float64(148) {
		t.Fatalf("answer: %q %s %s %v", text, model, provider, u)
	}
	usage := ReadUsage(raw)
	if usage.Incomplete != "" || len(usage.Reports) != 3 {
		t.Fatalf("usage: %+v", usage)
	}
	total := float64(0)
	for _, r := range usage.Reports {
		total += r.Usage.Total
	}
	if total != 383 {
		t.Fatalf("completed messages counted more than once: %v", total)
	}
	if LastModelError(raw) != "" {
		t.Fatal("successful tools and resume read as a model error")
	}
}
