package gitexec

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flokkit.io/forge/internal/execfile"
)

// scriptedGit puts a git first on PATH that runs script.
func scriptedGit(t *testing.T, script string) {
	t.Helper()
	bin := t.TempDir()
	if err := execfile.Write(filepath.Join(bin, "git"), []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// The environment Command is given is the whole of git's: a variable the
// process has does not arrive, and a nil environment is an empty one, never
// the process's own. Not parallel: it sets the process's environment.
func TestCommandEnvironmentIsExactlyTheOneGiven(t *testing.T) {
	scriptedGit(t, "/usr/bin/env\n")
	t.Setenv("FORGE_INHERITED_PROBE", "inherited")
	t.Setenv("GIT_DIR", "/elsewhere/.git")
	out, err := Command(context.Background(), t.TempDir(), []string{"FORGE_GIVEN=given"}, "status").Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := string(out); !strings.Contains(got, "FORGE_GIVEN=given\n") || strings.Contains(got, "FORGE_INHERITED_PROBE") || strings.Contains(got, "GIT_DIR") {
		t.Fatalf("git's environment was not the one given:\n%s", got)
	}
	out, err = Command(context.Background(), t.TempDir(), nil, "status").Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "FORGE_INHERITED_PROBE") || strings.Contains(string(out), "GIT_DIR") {
		t.Fatalf("a nil environment let the process's own through:\n%s", out)
	}
}
