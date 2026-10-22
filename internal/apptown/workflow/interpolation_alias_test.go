package workflow

import (
	"context"
	"testing"
)

// ${{workItem.*}} is the older name of ${{change.*}}. A save refuses it
// (schema.ParseWorkflow), but a stored workflow version a project adopted
// while it was current still runs, so it still reads the change.
func TestAStoredWorkflowsOldChangeNameStillReadsTheChange(t *testing.T) {
	t.Parallel()
	item := &Change{Title: "Fix the login page", Type: "bug"}
	got := Interpolate(context.Background(), nil, "${{workItem.title}} / ${{ change.type }}", nil, item, "")
	if got != "Fix the login page / bug" {
		t.Fatalf("interpolated %q", got)
	}
}
