package scm

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/gitexec/gittest"
)

// What a change changes is its own work: not what it merged from main when
// it caught up, before or after it landed. ReadChangeDiff and every caller
// of ChangeBase show only the change's files.
func TestAChangeThatCaughtUpWithMainChangesOnlyItsOwnFiles(t *testing.T) {
	t.Parallel()
	hub := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := gittest.Command(args...)
		cmd.Dir = hub
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
		return strings.TrimSpace(string(out))
	}
	commit := func(file string) string {
		t.Helper()
		if err := os.WriteFile(filepath.Join(hub, file), []byte(file), 0o644); err != nil {
			t.Fatal(err)
		}
		git("add", "-A")
		git("commit", "-q", "-m", file)
		return git("rev-parse", "HEAD")
	}
	ctx := context.Background()
	files := func(base string) string {
		t.Helper()
		return git("diff", "--name-only", base+"..refs/heads/apptown/change-1")
	}
	git("init", "-q", "-b", "main")
	git("config", "user.email", "t@t")
	git("config", "user.name", "t")
	recorded := commit("README.md")
	git("checkout", "-q", "-b", "apptown/change-1")
	commit("fix.go")
	git("checkout", "-q", "main")
	commit("main_test.go")
	git("checkout", "-q", "apptown/change-1")
	git("merge", "-q", "--no-edit", "-m", "Merge main into apptown/change-1", "main")
	commit("fix_test.go")

	base, err := ChangeBase(ctx, hub, "main", "refs/heads/apptown/change-1", recorded)
	if err != nil || files(base) != "fix.go\nfix_test.go" {
		t.Fatalf("caught up: base %s %v, files %q", base, err, files(base))
	}
	diff, err := ReadChangeDiff(ctx, hub, hub, "main", recorded, "apptown/change-1")
	if err != nil || diff.Base != base || !strings.Contains(diff.Stat, "2 files changed") {
		t.Fatalf("the change's diff: %+v %v", diff, err)
	}

	// Main moves again and the change lands with a merge: its base is still
	// the main it held before the landing, not its own tip.
	git("checkout", "-q", "main")
	commit("later.go")
	git("merge", "-q", "--no-ff", "-m", "land", "apptown/change-1")
	commit("after.go")
	if base, err := ChangeBase(ctx, hub, "main", "refs/heads/apptown/change-1", recorded); err != nil || files(base) != "fix.go\nfix_test.go" {
		t.Fatalf("landed: base %s %v, files %q", base, err, files(base))
	}

	// With no default branch to compare with, the recorded base stands in;
	// with neither, the base cannot be read.
	if base, err := ChangeBase(ctx, hub, "trunk", "refs/heads/apptown/change-1", recorded); err != nil || base != recorded {
		t.Fatalf("no default branch: %s %v", base, err)
	}
	if _, err := ChangeBase(ctx, hub, "trunk", "refs/heads/apptown/change-1", ""); err == nil {
		t.Fatal("a base nobody can name was read")
	}
}

// A gate's base is the fork Git reads, and only Git saying there is none
// lets the recorded base stand in. An active change whose branch is an old
// main commit, after main moved on, forked there: main's later work is not
// its own (a historical view of the same change still begins where it
// recorded). A tip the hub does not hold yet takes the recorded base. Git
// failing to answer is an error for both, never the recorded base.
//
// Kills: a failed merge-base or ref read taken as "no history in common"
// (fallback audit F18).
func TestAGatesBaseIsGitsForkOrAnError(t *testing.T) {
	t.Parallel()
	hub := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := gittest.Command(args...)
		cmd.Dir = hub
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
		return strings.TrimSpace(string(out))
	}
	commit := func(file string) string {
		t.Helper()
		if err := os.WriteFile(filepath.Join(hub, file), []byte(file), 0o644); err != nil {
			t.Fatal(err)
		}
		git("add", "-A")
		git("commit", "-q", "-m", file)
		return git("rev-parse", "HEAD")
	}
	ctx := context.Background()
	git("init", "-q", "-b", "main")
	git("config", "user.email", "t@t")
	git("config", "user.name", "t")
	recorded := commit("a")
	caughtUp := commit("b")
	git("branch", "apptown/change-1") // caught up with main, nothing of its own yet
	commit("c")
	if base, err := ForkBase(ctx, hub, "main", "refs/heads/apptown/change-1", recorded); err != nil || base != caughtUp {
		t.Fatalf("an active change at an old main commit: base %s %v, want %s", base, err, caughtUp)
	}
	const elsewhere = "0123456789abcdef0123456789abcdef01234567"
	if base, err := ForkBase(ctx, hub, "main", elsewhere, recorded); err != nil || base != recorded {
		t.Fatalf("a tip the hub does not hold: %s %v", base, err)
	}

	// Git cannot read the history between main and the change: the commit
	// they share is gone from the object store.
	git("checkout", "-q", "-b", "apptown/change-2", caughtUp)
	tip := commit("d")
	shared := git("rev-parse", caughtUp)
	if err := os.Remove(filepath.Join(hub, ".git", "objects", shared[:2], shared[2:])); err != nil {
		t.Fatal(err)
	}
	for name, read := range map[string]func(context.Context, string, string, string, string) (string, error){"ForkBase": ForkBase, "ChangeBase": ChangeBase} {
		if base, err := read(ctx, hub, "main", tip, recorded); err == nil {
			t.Fatalf("%s: unreadable history gave base %s", name, base)
		}
		if base, err := read(ctx, filepath.Join(hub, "not-a-repository"), "main", tip, recorded); err == nil {
			t.Fatalf("%s: a hub Git cannot open gave base %s", name, base)
		}
	}
}
