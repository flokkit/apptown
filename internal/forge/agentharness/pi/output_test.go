package pi

import (
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
