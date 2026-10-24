package repositoryedit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/workflow"
)

// A task is given the run's published branch, or the default branch when
// the run's is not published yet; a hub Git cannot read is an error that
// says so, never taken for a branch that is not there.
func TestATaskSourceThatCannotBeReadIsAnErrorNotAnAbsentBranch(t *testing.T) {
	t.Parallel()
	data := t.TempDir()
	source := workflow.Source{Slug: "shop", DefaultBranch: "main", Branch: "apptown/run-1"}
	if err := os.MkdirAll(filepath.Join(data, "workspaces", "project-shop"), 0o700); err != nil { // a hub that is not a repository
		t.Fatal(err)
	}
	got, err := ProposeTaskSource(t.Context(), data, source)
	if err == nil || !strings.Contains(err.Error(), "reading the commit to give the task") || strings.Contains(err.Error(), "has no commit") {
		t.Fatalf("an unreadable hub gave %+v, %v", got, err)
	}
}
