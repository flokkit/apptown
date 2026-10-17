package verification

import (
	"testing"

	"flokkit.io/forge/internal/strs"
)

func TestFirstNonEmpty(t *testing.T) {
	t.Parallel()
	if strs.FirstNonBlank("", "  ", "x", "y") != "x" {
		t.Fatal("first non-blank not returned")
	}
	if strs.FirstNonBlank("", "   ") != "" {
		t.Fatal("all-blank should be empty")
	}
}
