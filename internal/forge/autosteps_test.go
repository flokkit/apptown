package forge

import (
	"slices"
	"testing"
)

// One dispatch table: every type the engine runs is in it, the bound list is
// derived from it, and the two types the resume query used to omit are
// present, as is the one "try again" refused.
func TestAutoStepTableIsComplete(t *testing.T) {
	t.Parallel()
	for _, typ := range []string{"script", "agent/pi", "scm/mr", "preview", "deploy", "workflow/call", "scan", "tests", "reconcile", "agent/review", "guidance/conformance"} {
		if !isAutoStep(typ) {
			t.Errorf("%s must be an auto step", typ)
		}
		if !slices.Contains(autoStepTypes(), typ) {
			t.Errorf("%s missing from the bound list", typ)
		}
	}
	for _, typ := range []string{"approval", "artifact/require", "adr/require", "scm/merged"} {
		if isAutoStep(typ) {
			t.Errorf("%s is not an auto step", typ)
		}
	}
}
