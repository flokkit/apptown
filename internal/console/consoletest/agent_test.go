package consoletest

import (
	"context"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/console"
)

// A script the fake runs, standing in for one in a container, sees none of
// the GIT_* variables the test inherited (a test run from a Git hook has
// the hook's GIT_DIR), and the variables it is given. Not parallel: it
// sets the process's environment.
func TestAScriptSeesNoInheritedGitVariable(t *testing.T) {
	t.Setenv("GIT_DIR", "/elsewhere/.git")
	t.Setenv("GIT_INDEX_FILE", "/elsewhere/index")
	var out strings.Builder
	in := console.Launch{ID: "a", Workspace: console.Workspace{Path: t.TempDir()}}
	if err := NewAgent().Script(context.Background(), in, "env", []string{"GIVEN=yes"}, &out); err != nil {
		t.Fatal(err)
	}
	env := "\n" + out.String()
	if strings.Contains(env, "\nGIT_DIR=") || strings.Contains(env, "\nGIT_INDEX_FILE=") {
		t.Fatalf("an inherited GIT_* variable reached the script:\n%s", out.String())
	}
	if !strings.Contains(env, "\nGIVEN=yes\n") {
		t.Fatalf("the script's own variable is missing:\n%s", out.String())
	}
}
