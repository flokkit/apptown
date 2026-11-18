package scm

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/gitexec/gittest"
)

// mainMergeHub is a hub whose main and change branch both start at base;
// write commits files to a branch there.
func mainMergeHub(t *testing.T) (hub string, write func(branch string, files map[string]string) string) {
	t.Helper()
	hub = filepath.Join(t.TempDir(), "hub")
	gittest.RunAsTester(t, t.TempDir(), "init", "-q", "-b", "main", hub)
	write = func(branch string, files map[string]string) string {
		t.Helper()
		if gittest.RunAsTester(t, hub, "branch", "--show-current") != branch {
			gittest.RunAsTester(t, hub, "checkout", "-q", branch)
		}
		for name, body := range files {
			if err := os.WriteFile(filepath.Join(hub, name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		gittest.RunAsTester(t, hub, "add", "-A")
		gittest.RunAsTester(t, hub, "commit", "-q", "-m", "to "+branch)
		return gittest.RunAsTester(t, hub, "rev-parse", "HEAD")
	}
	write("main", map[string]string{"app.txt": "one\ntwo\nthree\n"})
	gittest.RunAsTester(t, hub, "branch", "change")
	return hub, write
}

// AppTown's catch-up merge: a branch behind main gets an ordinary merge
// commit with the branch first and main second, holding both sides' work,
// made by AppTown and moving no ref; a branch that contains main, or a
// repository without main, needs none; a merge with conflicts is not made,
// and the files are named.
func TestAppTownMergesMainOnlyWhenTheBranchIsBehindAndTheMergeIsClean(t *testing.T) {
	ctx := context.Background()
	// AppTown's clock, far from the machine's.
	at := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
	hub, write := mainMergeHub(t)
	tip := write("change", map[string]string{"change.txt": "the change\n"})

	if got, err := MergeMain(ctx, hub, tip, "main", "Merge main into change\n", at); err != nil || got.Commit != "" || got.Main != "" || len(got.Conflicts) != 0 {
		t.Fatalf("a branch containing main needs no merge: %+v %v", got, err)
	}
	if got, err := MergeMain(ctx, hub, tip, "trunk", "Merge trunk into change\n", at); err != nil || got.Commit != "" || len(got.Conflicts) != 0 {
		t.Fatalf("a repository without the main branch needs no merge: %+v %v", got, err)
	}

	main := write("main", map[string]string{"app.txt": "one\ntwo\nthree\nfour\n", "added.txt": "on main\n"})
	before := gittest.RunAsTester(t, hub, "for-each-ref")
	got, err := MergeMain(ctx, hub, tip, "main", "Merge main into change\n", at)
	if err != nil || got.Commit == "" || got.Main != main || len(got.Conflicts) != 0 {
		t.Fatalf("a clean merge of main: %+v %v", got, err)
	}
	if parents := strings.Fields(gittest.RunAsTester(t, hub, "rev-list", "--parents", "-n", "1", got.Commit)); !slices.Equal(parents, []string{got.Commit, tip, main}) {
		t.Fatalf("the merge's parents are %v, want the branch then main", parents[1:])
	}
	if who := gittest.RunAsTester(t, hub, "log", "-1", "--format=%an <%ae> / %cn <%ce> / %s", got.Commit); who != "apptown <apptown@localhost> / apptown <apptown@localhost> / Merge main into change" {
		t.Fatalf("the merge is %q", who)
	}
	if dates := gittest.RunAsTester(t, hub, "log", "-1", "--format=%at %ct", got.Commit); dates != fmt.Sprintf("%d %d", at.Unix(), at.Unix()) {
		t.Fatalf("the merge is dated %q, want AppTown's clock", dates)
	}
	for name, want := range map[string]string{"app.txt": "one\ntwo\nthree\nfour\n", "added.txt": "on main\n", "change.txt": "the change\n"} {
		if body := gittest.RunAsTester(t, hub, "show", got.Commit+":"+name); body != strings.TrimSpace(want) {
			t.Errorf("%s in the merge = %q", name, body)
		}
	}
	if after := gittest.RunAsTester(t, hub, "for-each-ref"); after != before {
		t.Fatalf("making the merge moved a ref:\n%s\nthen\n%s", before, after)
	}

	conflicting := write("change", map[string]string{"app.txt": "one\nTWO\nthree\n"})
	write("main", map[string]string{"app.txt": "one\n2\nthree\nfour\n"})
	got, err = MergeMain(ctx, hub, conflicting, "main", "Merge main into change\n", at)
	if err != nil || got.Commit != "" || !slices.Equal(got.Conflicts, []string{"app.txt"}) {
		t.Fatalf("a merge with conflicts names the files and makes nothing: %+v %v", got, err)
	}
}

// A standing acceptance survives a landing's catch-up only when the
// candidate is the accepted commit with main cleanly merged in: one or
// several of AppTown's own catch-up merges pass; a merge with the right
// parents but an edit of its own, a commit on top, or another history is
// refused with a reason.
func TestCleanCatchUpAcceptsOnlyMainMergedIntoTheAcceptedCommit(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
	hub, write := mainMergeHub(t)
	accepted := write("change", map[string]string{"go.mod": "module app\nrequire dep v1.2.4\n"})
	write("main", map[string]string{"other.txt": "another change landed\n"})
	catchUp := func(tip string) string {
		t.Helper()
		merge, err := MergeMain(ctx, hub, tip, "main", "Merge main into change\n", at)
		if err != nil || merge.Commit == "" {
			t.Fatalf("catch-up: %+v %v", merge, err)
		}
		return merge.Commit
	}
	once := catchUp(accepted)
	target := gittest.RunAsTester(t, hub, "rev-parse", "main")
	if why, err := CleanCatchUp(ctx, hub, accepted, once, target); err != nil || why != "" {
		t.Fatalf("a clean catch-up was refused: %q %v", why, err)
	}
	if why, err := CleanCatchUp(ctx, hub, accepted, accepted, target); err != nil || why != "" {
		t.Fatalf("the accepted commit itself was refused: %q %v", why, err)
	}
	write("main", map[string]string{"third.txt": "main moved again\n"})
	twice := catchUp(once)
	later := gittest.RunAsTester(t, hub, "rev-parse", "main")
	if why, err := CleanCatchUp(ctx, hub, accepted, twice, later); err != nil || why != "" {
		t.Fatalf("a second catch-up was refused: %q %v", why, err)
	}
	if why, err := CleanCatchUp(ctx, hub, accepted, twice, target); err != nil || why == "" {
		t.Fatalf("a merge of commits beyond the pinned target passed: %q %v", why, err)
	}

	// The right parents, and an application edit inside the merge.
	gittest.RunAsTester(t, hub, "checkout", "-q", "--detach", accepted)
	gittest.RunAsTester(t, hub, "merge", "-q", "--no-commit", "--no-ff", target)
	if err := os.WriteFile(filepath.Join(hub, "app.go"), []byte("package app // not accepted\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.RunAsTester(t, hub, "add", "-A")
	gittest.RunAsTester(t, hub, "commit", "-q", "-m", "Merge main into change")
	evil := gittest.RunAsTester(t, hub, "rev-parse", "HEAD")
	if parents := strings.Fields(gittest.RunAsTester(t, hub, "rev-list", "--parents", "-n", "1", evil)); !slices.Equal(parents, []string{evil, accepted, target}) {
		t.Fatalf("the edited merge's parents are %v", parents)
	}
	if why, err := CleanCatchUp(ctx, hub, accepted, evil, target); err != nil || !strings.Contains(why, "changes more than merging main in") {
		t.Fatalf("a merge carrying its own edit passed: %q %v", why, err)
	}
	if why, err := CleanCatchUp(ctx, hub, accepted, catchUp(evil), later); err != nil || why == "" {
		t.Fatalf("an edited merge under a clean catch-up passed: %q %v", why, err)
	}

	// A commit on top of a clean catch-up, and a history without the accepted commit.
	gittest.RunAsTester(t, hub, "checkout", "-q", "--detach", once)
	if err := os.WriteFile(filepath.Join(hub, "app.go"), []byte("package app // published later\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.RunAsTester(t, hub, "add", "-A")
	gittest.RunAsTester(t, hub, "commit", "-q", "-m", "more work")
	onTop := gittest.RunAsTester(t, hub, "rev-parse", "HEAD")
	if why, err := CleanCatchUp(ctx, hub, accepted, onTop, target); err != nil || !strings.Contains(why, "is not a merge of main") {
		t.Fatalf("a commit on top of the catch-up passed: %q %v", why, err)
	}
	if why, err := CleanCatchUp(ctx, hub, accepted, target, target); err != nil || why == "" {
		t.Fatalf("main's own history passed as the accepted commit's: %q %v", why, err)
	}
}
