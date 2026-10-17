package check_test

import (
	"strings"
	"testing"

	"flokkit.io/forge/internal/check"
)

func TestValidateChecksTool(t *testing.T) {
	t.Parallel()
	// tool with a binding name is valid; evaluator defaults to "tool".
	ok := []check.Declaration{{Name: "sast", Tool: "semgrep-django", Expects: "clean"}}
	if err := check.Validate(ok); err != nil {
		t.Fatalf("valid tool check rejected: %v", err)
	}
	if ok[0].Evaluator != "tool" {
		t.Fatalf("evaluator not defaulted to tool: %q", ok[0].Evaluator)
	}
	// tool evaluator with no binding name errors.
	bad := []check.Declaration{{Name: "sast", Evaluator: "tool"}}
	if err := check.Validate(bad); err == nil || !strings.Contains(err.Error(), "tool") {
		t.Fatalf("expected tool-requires-name error, got %v", err)
	}
	// unknown evaluator names all four.
	if err := check.Validate([]check.Declaration{{Name: "x", Evaluator: "bogus"}}); err == nil ||
		!strings.Contains(err.Error(), "sh, llm, runtime or tool") {
		t.Fatalf("evaluator message not updated: %v", err)
	}
}
