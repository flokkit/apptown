package conversation

import (
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/workflow"
)

func TestIntentLineSaysWhenNothingWasWritten(t *testing.T) {
	t.Parallel()
	if got := intentLine(&workflow.Change{Title: "First version", Body: "  "}); !strings.Contains(got, "nothing written") || !strings.Contains(got, "ask the person") {
		t.Fatalf("empty body must read as 'no brief, ask': %q", got)
	}
	if got := intentLine(&workflow.Change{Title: "x", Body: "a real brief"}); got != "a real brief" {
		t.Fatalf("a written body must pass through: %q", got)
	}
}
