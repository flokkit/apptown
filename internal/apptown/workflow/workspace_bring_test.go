package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/flokkit/apptown/internal/execution"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/gitexec/gittest"
)

func catchUpGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := gittest.Command(append([]string{"-c", "user.name=t", "-c", "user.email=t@example.org"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// catchUpWorld is a project repository with branch work and a change's
// folder cloned from it.
func catchUpWorld(t *testing.T) (repo, folder string) {
	t.Helper()
	repo, folder = t.TempDir(), filepath.Join(t.TempDir(), "change")
	catchUpGit(t, repo, "init", "-q", "-b", "main")
	write := func(dir, name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(repo, "a.txt", "one\n")
	catchUpGit(t, repo, "add", "a.txt")
	catchUpGit(t, repo, "commit", "-qm", "base")
	catchUpGit(t, repo, "branch", "work")
	catchUpGit(t, "", "clone", "-q", "--no-hardlinks", "-b", "work", repo, folder)
	return repo, folder
}

func commitIn(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	catchUpGit(t, dir, "add", name)
	catchUpGit(t, dir, "commit", "-qm", name)
}

// A change's folder is brought to its branch as published before a new
// holder works in it: fast-forwarded when it has nothing of its own, left as
// it is when it already contains the branch or the branch was never
// published, and never merged: when both have moved, nothing changes and
// ErrFolderDiverged says so.
func TestAChangesFolderIsBroughtToItsPublishedBranch(t *testing.T) {
	g, ctx := GitWorkspace{}, context.Background()
	t.Run("fast-forward", func(t *testing.T) {
		repo, folder := catchUpWorld(t)
		catchUpGit(t, repo, "checkout", "-q", "work")
		commitIn(t, repo, "b.txt", "published by a run\n")
		if err := g.BringToBranch(ctx, folder, repo, "work"); err != nil {
			t.Fatal(err)
		}
		if catchUpGit(t, folder, "rev-parse", "work") != catchUpGit(t, repo, "rev-parse", "work") {
			t.Fatal("the folder was not brought to the branch")
		}
	})
	t.Run("already contains it", func(t *testing.T) {
		repo, folder := catchUpWorld(t)
		commitIn(t, folder, "c.txt", "the chat's own commit\n")
		before := catchUpGit(t, folder, "rev-parse", "HEAD")
		if err := g.BringToBranch(ctx, folder, repo, "work"); err != nil {
			t.Fatal(err)
		}
		if after := catchUpGit(t, folder, "rev-parse", "HEAD"); after != before {
			t.Fatalf("the folder moved from %s to %s", before, after)
		}
	})
	t.Run("both moved", func(t *testing.T) {
		repo, folder := catchUpWorld(t)
		catchUpGit(t, repo, "checkout", "-q", "work")
		commitIn(t, repo, "b.txt", "published by a desktop push\n")
		commitIn(t, folder, "c.txt", "the chat's own commit\n")
		before := catchUpGit(t, folder, "rev-parse", "HEAD")
		if err := g.BringToBranch(ctx, folder, repo, "work"); !errors.Is(err, ErrFolderDiverged) {
			t.Fatalf("a diverged folder: %v", err)
		}
		if after := catchUpGit(t, folder, "rev-parse", "HEAD"); after != before {
			t.Fatalf("the folder moved from %s to %s", before, after)
		}
		if _, err := os.Stat(filepath.Join(folder, "b.txt")); err == nil {
			t.Fatal("the branch's work was merged in")
		}
	})
	t.Run("never published", func(t *testing.T) {
		repo, folder := catchUpWorld(t)
		catchUpGit(t, repo, "branch", "-D", "work")
		if err := g.BringToBranch(ctx, folder, repo, "work"); err != nil {
			t.Fatalf("a branch never published: %v", err)
		}
	})
}

// Exercise the same Runner boundary as fetch, not only gitexec.Command. The
// repository opts into detachment to prove AppTown's command policy wins.
func TestWorkspaceGitKeepsMaintenanceInsideItsCommand(t *testing.T) {
	repo, folder := catchUpWorld(t)
	for _, key := range []string{"maintenance.autoDetach", "gc.autoDetach"} {
		catchUpGit(t, folder, "config", key, "true")
		got, err := (GitWorkspace{}).git(t.Context(), folder, "config", "--get", key)
		if err != nil || got != "false" {
			t.Errorf("workspace Git did not enforce foreground %s: got %q, %v", key, got, err)
		}
	}
	catchUpGit(t, repo, "checkout", "-q", "work")
	commitIn(t, repo, "b.txt", "published work\n")
	commitIn(t, folder, "c.txt", "local work\n")
	before := catchUpGit(t, folder, "rev-parse", "HEAD")
	trace := filepath.Join(t.TempDir(), "fetch-trace.jsonl")
	g := GitWorkspace{Runner: processFunc(func(ctx context.Context, backend string, req execution.Request) execution.Outcome {
		req.Env = append(req.Env, "GIT_TRACE2_EVENT="+trace)
		return (execution.Runner{}).Run(ctx, backend, req)
	})}
	if err := g.BringToBranch(t.Context(), folder, repo, "work"); !errors.Is(err, ErrFolderDiverged) {
		t.Fatalf("diverged fetch: %v", err)
	}
	if got := catchUpGit(t, folder, "rev-parse", "HEAD"); got != before {
		t.Fatal("diverged fetch moved local work")
	}
	raw, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	maintenance := 0
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var event struct {
			Event string   `json:"event"`
			Argv  []string `json:"argv"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		if event.Event != "child_start" {
			continue
		}
		for i, arg := range event.Argv {
			if arg != "maintenance" || i+1 >= len(event.Argv) || event.Argv[i+1] != "run" {
				continue
			}
			maintenance++
			t.Logf("workspace maintenance argv: %q", event.Argv)
			for _, arg := range event.Argv {
				if arg == "--detach" {
					t.Errorf("workspace fetch maintenance escaped its command: %v", event.Argv)
				}
			}
		}
	}
	t.Logf("observed %d automatic maintenance invocations through workspace fetch", maintenance)
	// Older Git may not emit this child. Effective config above is still a
	// deterministic control; maintained Linux qualification requires the trace.
}
