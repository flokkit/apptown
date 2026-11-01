package docker

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/flokkit/apptown/internal/gitexec/gittest"
)

// The build context must carry the worktree's TRACKED state (committed +
// uncommitted edits) but NEVER untracked/ignored cruft — a mise .venv with
// host-path symlinks, caches/gitignored secrets, or derived files.
func TestStagedBuildContextExcludesUntracked(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	git := func(args ...string) {
		c := gittest.Command(args...)
		c.Dir = ws
		c.Env = append(c.Env, "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	git("init", "-q")
	write := func(rel, body string) {
		p := filepath.Join(ws, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("Dockerfile", "FROM scratch")
	write(".gitignore", ".venv/\n")
	git("add", "Dockerfile", ".gitignore")
	git("commit", "-qm", "seed")

	// Untracked, gitignored cruft: a .venv with a symlink to a host path that
	// does not exist inside a container — the exact office-poll failure.
	_ = os.MkdirAll(filepath.Join(ws, ".venv", "bin"), 0o755)
	_ = os.Symlink("/apptown-cache/mise/installs/python/3.12/bin/python3", filepath.Join(ws, ".venv", "bin", "python"))
	// An uncommitted edit to a TRACKED file — must be carried into the context.
	write("Dockerfile", "FROM scratch\n# edited")

	ctx, cleanup, err := stagedBuildContext(t.Context(), ws, "")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	if ctx == filepath.Join(ws, "") {
		t.Fatal("stagedBuildContext returned the live worktree (fell back) — untracked cruft would leak")
	}
	if _, err := os.Stat(filepath.Join(ctx, ".venv")); !os.IsNotExist(err) {
		t.Fatalf("untracked .venv leaked into the build context (err=%v)", err)
	}
	body, err := os.ReadFile(filepath.Join(ctx, "Dockerfile"))
	if err != nil {
		t.Fatalf("tracked Dockerfile missing from context: %v", err)
	}
	if string(body) != "FROM scratch\n# edited" {
		t.Fatalf("uncommitted edit to a tracked file was dropped; got %q", body)
	}
}

func TestBuildStagingFailureDoesNotUseLiveWorktree(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	if err := os.Mkdir(filepath.Join(ws, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cleanup, err := stagedBuildContext(t.Context(), ws, ".")
	if err == nil || ctx != "" || cleanup != nil {
		t.Fatalf("broken Git repository must not become a live build: %q %v", ctx, err)
	}
}

// A worker receives the change folder as a copy at another path, so the
// index's stat data no longer matches the files. A clean copy must build
// its commit and a dirty copy its tracked edits; neither may fail.
func TestStagedBuildContextSelectsSourceInCopiedFolder(t *testing.T) {
	t.Parallel()
	origin := filepath.Join(t.TempDir(), "origin")
	if err := os.Mkdir(origin, 0o755); err != nil {
		t.Fatal(err)
	}
	// Git through gittest, whose maintenance ends with the command: a
	// detached "maintenance run --auto" after the commit held
	// objects/maintenance.lock while cp copied the repository (CI, 23 Oct).
	git := func(args ...string) {
		c := gittest.Command(args...)
		c.Dir = origin
		c.Env = append(c.Env, "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(origin, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("init", "-q")
	git("add", "Dockerfile")
	git("commit", "-qm", "seed")
	copyOf := func() string {
		dst := filepath.Join(t.TempDir(), "copy")
		if out, err := exec.Command("cp", "-a", origin, dst).CombinedOutput(); err != nil {
			t.Fatalf("copying the repository: %v: %s", err, out)
		}
		return dst
	}
	read := func(ws string) string {
		ctx, cleanup, err := stagedBuildContext(t.Context(), ws, ".")
		if err != nil {
			t.Fatalf("selecting the build source in a copied folder: %v", err)
		}
		defer cleanup()
		body, err := os.ReadFile(filepath.Join(ctx, "Dockerfile"))
		if err != nil {
			t.Fatal(err)
		}
		return string(body)
	}

	if got := read(copyOf()); got != "FROM scratch\n" {
		t.Fatalf("clean copy built %q, want the commit", got)
	}
	dirty := copyOf()
	if err := os.WriteFile(filepath.Join(dirty, "Dockerfile"), []byte("FROM scratch\n# edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := read(dirty); got != "FROM scratch\n# edited\n" {
		t.Fatalf("dirty copy built %q, want the tracked edit", got)
	}
}
