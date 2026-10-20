package scm

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"flokkit.io/forge/internal/gitexec/gittest"
)

// A new project repository gets the forge's identity from git init itself
// (a template holding only that config) rather than from two config
// commands after it. Its config is the one init and the two commands
// made, its HEAD is the default branch, and its first commit is the
// forge's. A repository that already exists is given the identity as
// before, over whatever it had.
func TestANewProjectRepositoryStartsWithTheForgeIdentity(t *testing.T) {
	t.Parallel()
	config := func(dir string) string {
		t.Helper()
		entries := strings.Split(gittest.Run(t, dir, "config", "--local", "--list"), "\n")
		slices.Sort(entries)
		return strings.Join(entries, "\n")
	}
	dir := filepath.Join(t.TempDir(), "project-x")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := InitializeRepository(t.Context(), dir, "trunk"); err != nil {
		t.Fatal(err)
	}
	old := t.TempDir()
	gittest.Run(t, old, "init", "-q", "-b", "trunk")
	gittest.Run(t, old, "config", "user.name", "apptown")
	gittest.Run(t, old, "config", "user.email", "apptown@localhost")
	if got, want := config(dir), config(old); got != want {
		t.Fatalf("config is\n%s\nwant\n%s", got, want)
	}
	if got := gittest.Run(t, dir, "symbolic-ref", "HEAD"); got != "refs/heads/trunk" {
		t.Fatalf("HEAD is %s", got)
	}
	if got := gittest.Run(t, dir, "log", "-1", "--format=%an <%ae> %cn <%ce> %s"); got != "apptown <apptown@localhost> apptown <apptown@localhost> apptown: adopt existing workspace" {
		t.Fatalf("the first commit is %q", got)
	}
	if got := gittest.Run(t, dir, "ls-files"); got != ".gitignore\nmain.go" {
		t.Fatalf("the first commit holds %q", got)
	}

	existing := t.TempDir()
	gittest.Run(t, existing, "init", "-q", "-b", "main")
	gittest.Run(t, existing, "config", "user.name", "someone")
	gittest.Run(t, existing, "config", "user.email", "someone@example.invalid")
	if err := InitializeRepository(t.Context(), existing, "main"); err != nil {
		t.Fatal(err)
	}
	if got := gittest.Run(t, existing, "log", "-1", "--format=%an <%ae>"); got != "apptown <apptown@localhost>" {
		t.Fatalf("an existing repository's first commit is %q", got)
	}
}

// A repository made from the forge's own template still has the hook that
// refuses every push (InstallNoPushHook): a plain push to main, or to a
// new branch, from a clone is refused and the repository is unchanged. The
// push runs without the machine's config, so a personal core.hooksPath
// cannot switch the hook off here.
func TestANewProjectRepositoryRefusesAPlainPush(t *testing.T) {
	t.Parallel()
	hub := filepath.Join(t.TempDir(), "project-x")
	if err := InitializeRepository(t.Context(), hub, "main"); err != nil {
		t.Fatal(err)
	}
	before := gittest.Run(t, hub, "for-each-ref", "--format=%(refname) %(objectname)")
	clone := filepath.Join(t.TempDir(), "clone")
	gittest.Run(t, filepath.Dir(clone), "clone", "-q", hub, clone)
	gittest.RunAsTester(t, clone, "commit", "-q", "--allow-empty", "-m", "pushed")
	for _, target := range []string{"HEAD:main", "HEAD:refs/heads/elsewhere"} {
		push := gittest.Command("push", "-q", "origin", target)
		push.Dir = clone
		push.Env = append(push.Env, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		out, err := push.CombinedOutput()
		if err == nil || !strings.Contains(string(out), "pushing to the hub is refused") {
			t.Fatalf("a push to %s: %v: %s", target, err, out)
		}
	}
	if after := gittest.Run(t, hub, "for-each-ref", "--format=%(refname) %(objectname)"); after != before {
		t.Fatalf("the repository's refs moved from\n%s\nto\n%s", before, after)
	}
}
