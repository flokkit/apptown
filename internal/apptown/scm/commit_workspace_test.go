package scm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/execfile"

	"github.com/flokkit/apptown/internal/gitexec/gittest"
)

// commitWorkspaceFolder is a step's folder as change 338's was: a project
// .gitignore with no .apptown rule, so the runtime files a step leaves under
// .apptown show in git status.
func commitWorkspaceFolder(t *testing.T) string {
	t.Helper()
	wt := filepath.Join(t.TempDir(), "wt")
	gittest.Repository(t, wt, "main", gittest.Commit{Message: "start", Files: map[string]string{
		".gitignore": "/bin/\n", "main.go": "package main\n", ".apptown/app.yaml": "app: {}\n",
	}})
	for rel, body := range map[string]string{
		".apptown/pi-home/settings.json":     "{}",
		".apptown/sessions/transcript.jsonl": "{}",
		".apptown/steering.md":               "steer",
	} {
		writeIn(t, wt, rel, body)
	}
	return wt
}

func writeIn(t *testing.T, wt, rel, body string) {
	t.Helper()
	p := filepath.Join(wt, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Change 338: the agent committed its own work, leaving only untracked
// runtime files under .apptown. The step has nothing to commit, which is not
// a failure.
//
// Kills: deciding to commit from the working tree's status.
func TestCommitWorkspaceWithOnlyRuntimeFilesLeftCommitsNothing(t *testing.T) {
	t.Parallel()
	wt := commitWorkspaceFolder(t)
	writeIn(t, wt, "main.go", "package main\n\nfunc main() {}\n")
	gittest.Run(t, wt, "commit", "-qam", "the agent's own commit")
	before := gittest.Run(t, wt, "rev-parse", "HEAD")

	sha, committed, err := CommitWorkspace(t.Context(), wt, "step")
	if err != nil || committed || sha != "" {
		t.Fatalf("CommitWorkspace = %q, %v, %v; want no commit and no error", sha, committed, err)
	}
	if after := gittest.Run(t, wt, "rev-parse", "HEAD"); after != before {
		t.Fatalf("HEAD moved from %s to %s", before, after)
	}
}

// Source the step changed and did not commit is committed, runtime files
// still left out; so is a change to the app contract alone, or its removal.
//
// Kills: dropping the commit when something is staged; StageAll's contract
// add; staging a removed contract's deletion.
func TestCommitWorkspaceCommitsChangedSourceAndTheAppContract(t *testing.T) {
	t.Parallel()
	for name, change := range map[string][2]string{
		"source":               {"main.go", "package main\n\nfunc main() {}\n"},
		"app contract":         {".apptown/app.yaml", "app:\n  web: {}\n"},
		"app contract removed": {".apptown/app.yaml", ""},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			wt := commitWorkspaceFolder(t)
			if change[1] == "" {
				if err := os.Remove(filepath.Join(wt, change[0])); err != nil {
					t.Fatal(err)
				}
			} else {
				writeIn(t, wt, change[0], change[1])
			}

			sha, committed, err := CommitWorkspace(t.Context(), wt, "step")
			if err != nil || !committed || sha == "" {
				t.Fatalf("CommitWorkspace = %q, %v, %v; want a commit", sha, committed, err)
			}
			if files := gittest.Run(t, wt, "show", "--name-only", "--format=", "HEAD"); files != change[0] {
				t.Fatalf("the commit holds %q, want only %s", files, change[0])
			}
		})
	}
}

// A commit git refuses is still an error, carrying what git said.
//
// Kills: treating a failed commit as nothing to commit.
func TestCommitWorkspaceReportsARefusedCommit(t *testing.T) {
	t.Parallel()
	wt := commitWorkspaceFolder(t)
	writeIn(t, wt, "main.go", "package main\n\nfunc main() {}\n")
	if err := execfile.Write(filepath.Join(wt, ".git/hooks/pre-commit"), []byte("#!/bin/sh\necho refused by hook >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	if _, committed, err := CommitWorkspace(t.Context(), wt, "step"); err == nil || committed || !strings.Contains(err.Error(), "refused by hook") {
		t.Fatalf("CommitWorkspace = %v, %v; want the refusal as an error", committed, err)
	}
}

// Adopting a repository whose own .gitignore does not hide .apptown, with
// only runtime files there, makes no commit and is not a failure.
//
// Kills: the adoption deciding to commit from the working tree's status.
func TestAdoptingARepositoryWithOnlyRuntimeFilesCommitsNothing(t *testing.T) {
	t.Parallel()
	wt := commitWorkspaceFolder(t)
	before := gittest.Run(t, wt, "rev-parse", "HEAD")

	if err := InitializeRepository(t.Context(), wt, "main"); err != nil {
		t.Fatal(err)
	}
	if after := gittest.Run(t, wt, "rev-parse", "HEAD"); after != before {
		t.Fatalf("HEAD moved from %s to %s", before, after)
	}
}

// Source git cannot stage, the app contract included, is still uncommitted
// source: the failure is an error, never an unchanged index read as nothing
// to commit.
//
// Kills: StageAll discarding either add's failure.
func TestCommitWorkspaceReportsSourceItCouldNotStage(t *testing.T) {
	t.Parallel()
	for _, rel := range []string{"main.go", ".apptown/app.yaml"} {
		t.Run(rel, func(t *testing.T) {
			t.Parallel()
			wt := commitWorkspaceFolder(t)
			writeIn(t, wt, rel, "changed\n")
			if err := os.Chmod(filepath.Join(wt, rel), 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(filepath.Join(wt, rel), 0o644) })

			if _, committed, err := CommitWorkspace(t.Context(), wt, "step"); err == nil || committed {
				t.Fatalf("CommitWorkspace = %v, %v; want the staging failure as an error", committed, err)
			}
		})
	}
}
