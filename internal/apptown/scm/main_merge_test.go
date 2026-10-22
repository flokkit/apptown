package scm

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

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
	hub, write := mainMergeHub(t)
	tip := write("change", map[string]string{"change.txt": "the change\n"})

	if got, err := MergeMain(ctx, hub, tip, "main", "Merge main into change\n"); err != nil || got.Commit != "" || got.Main != "" || len(got.Conflicts) != 0 {
		t.Fatalf("a branch containing main needs no merge: %+v %v", got, err)
	}
	if got, err := MergeMain(ctx, hub, tip, "trunk", "Merge trunk into change\n"); err != nil || got.Commit != "" || len(got.Conflicts) != 0 {
		t.Fatalf("a repository without the main branch needs no merge: %+v %v", got, err)
	}

	main := write("main", map[string]string{"app.txt": "one\ntwo\nthree\nfour\n", "added.txt": "on main\n"})
	before := gittest.RunAsTester(t, hub, "for-each-ref")
	got, err := MergeMain(ctx, hub, tip, "main", "Merge main into change\n")
	if err != nil || got.Commit == "" || got.Main != main || len(got.Conflicts) != 0 {
		t.Fatalf("a clean merge of main: %+v %v", got, err)
	}
	if parents := strings.Fields(gittest.RunAsTester(t, hub, "rev-list", "--parents", "-n", "1", got.Commit)); !slices.Equal(parents, []string{got.Commit, tip, main}) {
		t.Fatalf("the merge's parents are %v, want the branch then main", parents[1:])
	}
	if who := gittest.RunAsTester(t, hub, "log", "-1", "--format=%an <%ae> / %cn <%ce> / %s", got.Commit); who != "apptown <apptown@localhost> / apptown <apptown@localhost> / Merge main into change" {
		t.Fatalf("the merge is %q", who)
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
	got, err = MergeMain(ctx, hub, conflicting, "main", "Merge main into change\n")
	if err != nil || got.Commit != "" || !slices.Equal(got.Conflicts, []string{"app.txt"}) {
		t.Fatalf("a merge with conflicts names the files and makes nothing: %+v %v", got, err)
	}
}
