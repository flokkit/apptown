package deploy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flokkit.io/forge/internal/gitexec/gittest"
)

func sourceGit(t *testing.T, repo string, args ...string) string {
	t.Helper()
	out, err := gittest.Command(append([]string{"-C", repo}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s: %v", args, out, err)
	}
	return strings.TrimSpace(string(out))
}
func sourceFile(t *testing.T, root, name, body string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
func sourceCommit(t *testing.T, repo string) string {
	t.Helper()
	sourceGit(t, repo, "add", ".")
	sourceGit(t, repo, "commit", "-qm", "selected source")
	return sourceGit(t, repo, "rev-parse", "HEAD")
}
func sourceRepo(t *testing.T) (string, string) {
	t.Helper()
	repo := t.TempDir()
	sourceGit(t, repo, "init", "-q")
	sourceGit(t, repo, "config", "user.name", "test")
	sourceGit(t, repo, "config", "user.email", "test@example.com")
	sourceFile(t, repo, "index.html", "original game\n")
	return repo, sourceCommit(t, repo)
}
func assertSourceFile(t *testing.T, root, name, want string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, name))
	if err != nil || string(raw) != want {
		t.Fatalf("%s: %q %v, want %q", name, raw, err, want)
	}
}

func TestSelectedSourceStagesExactRevisionAndCleansUp(t *testing.T) {
	t.Parallel()
	repo, original := sourceRepo(t)
	sourceFile(t, repo, "index.html", "later game\n")
	sourceCommit(t, repo)
	sourceFile(t, repo, "index.html", "uncommitted edits\n")
	parent := t.TempDir()
	tree, cleanup, err := (SelectedSource{Repository: repo, Revision: original}).Stage(context.Background(), parent)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if tree.Revision != original {
		t.Fatalf("identity: %+v", tree)
	}
	assertSourceFile(t, tree.Directory, "index.html", "original game\n")
	if _, err := os.Stat(filepath.Join(tree.Directory, ".apptown/app.yaml")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invented contract: %v", err)
	}
	cleanup()
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 0 {
		t.Fatalf("staging leaked files: %v %v", entries, err)
	}
	assertSourceFile(t, repo, "index.html", "uncommitted edits\n")
}

func TestSelectedSourceRejectsInvalidRevisionWithoutExecutingIt(t *testing.T) {
	t.Parallel()
	repo, _ := sourceRepo(t)
	parent := t.TempDir()
	marker := filepath.Join(parent, "unexpected-command")
	for _, revision := range []string{"", strings.Repeat("0", 40), "HEAD $(touch " + marker + ")", "--help"} {
		tree, cleanup, err := (SelectedSource{Repository: repo, Revision: revision}).Stage(context.Background(), parent)
		cleanup()
		if err == nil || tree != (SourceTree{}) {
			t.Fatalf("invalid selection admitted: %q %+v %v", revision, tree, err)
		}
	}
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 0 {
		t.Fatalf("invalid selection created files: %v %v", entries, err)
	}
}

func TestPreviewSourceKeepsWorktreeEditsAndRefreshesHistoricalSnapshot(t *testing.T) {
	t.Parallel()
	repo, original := sourceRepo(t)
	snapshots := filepath.Join(t.TempDir(), "snapshots")
	in := PreviewSource{Worktree: repo, Snapshots: snapshots, History: SelectedSource{Repository: repo}}
	sourceFile(t, repo, "index.html", "live edits\n")
	tree, err := in.Prepare(context.Background())
	if err != nil || tree.Directory != repo || tree.Revision != original {
		t.Fatalf("worktree selection: %+v %v", tree, err)
	}
	assertSourceFile(t, tree.Directory, "index.html", "live edits\n")
	sourceFile(t, repo, "index.html", "later game\n")
	latest := sourceCommit(t, repo)
	in.Worktree = filepath.Join(t.TempDir(), "absent-worktree")
	in.History.Revision = original
	tree, err = in.Prepare(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	served := tree
	assertSourceFile(t, tree.Directory, "index.html", "original game\n")
	// Cache reuse preserves boot-generated files for the same selected revision.
	sourceFile(t, served.Directory, "boot-generated", "cache reuse")
	if _, err = in.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertSourceFile(t, served.Directory, "boot-generated", "cache reuse")
	// Another commit is a tree of its own: the one a preview serves stays as
	// it is until Retain, once nothing serves it.
	in.History.Revision = latest
	tree, err = in.Prepare(context.Background())
	if err != nil || tree.Revision != latest || tree.Directory == served.Directory {
		t.Fatalf("refreshed selection: %+v %v", tree, err)
	}
	assertSourceFile(t, tree.Directory, "index.html", "later game\n")
	assertSourceFile(t, served.Directory, "index.html", "original game\n")
	assertSourceFile(t, served.Directory, "boot-generated", "cache reuse")
	if err := in.Retain(tree); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(served.Directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("Retain kept a snapshot nothing serves")
	}
	assertSourceFile(t, tree.Directory, "index.html", "later game\n")
	for _, invalid := range []string{"", strings.Repeat("0", 40)} {
		in.History.Revision = invalid
		if got, err := in.Prepare(context.Background()); err == nil || got != (SourceTree{}) {
			t.Fatalf("invalid source reused cache: %+v %v", got, err)
		}
		assertSourceFile(t, tree.Directory, "index.html", "later game\n")
	}
}

func TestSnapshotMarkerDoesNotFollowSourceSymlinks(t *testing.T) {
	for _, kind := range []string{"marker", "directory"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			repo, original := sourceRepo(t)
			destination := filepath.Join(t.TempDir(), "snapshot")
			if _, err := (SelectedSource{Repository: repo, Revision: original}).Snapshot(context.Background(), destination); err != nil {
				t.Fatal(err)
			}
			outside := t.TempDir()
			sourceFile(t, outside, "snapshot-sha", "do not overwrite")
			name, target := filepath.Join(repo, ".apptown"), outside
			if kind == "marker" {
				if err := os.Mkdir(name, 0o755); err != nil {
					t.Fatal(err)
				}
				name, target = filepath.Join(name, "snapshot-sha"), filepath.Join(outside, "snapshot-sha")
				sourceFile(t, repo, ".apptown/app.yaml", "native contract bytes\n")
			}
			if err := os.Symlink(target, name); err != nil {
				t.Fatal(err)
			}
			revision := sourceCommit(t, repo)
			tree, err := (SelectedSource{Repository: repo, Revision: revision}).Snapshot(context.Background(), destination)
			if kind == "directory" {
				if err == nil {
					t.Fatal("source-supplied .apptown symlink accepted")
				}
				assertSourceFile(t, destination, ".apptown/snapshot-sha", original+"\n")
			} else {
				if err != nil {
					t.Fatal(err)
				}
				assertSourceFile(t, tree.Directory, ".apptown/snapshot-sha", revision+"\n")
				assertSourceFile(t, tree.Directory, ".apptown/app.yaml", "native contract bytes\n")
			}
			assertSourceFile(t, outside, "snapshot-sha", "do not overwrite")
		})
	}
}

func TestCancelledSourcePreparationDoesNotPublish(t *testing.T) {
	t.Parallel()
	repo, revision := sourceRepo(t)
	source := SelectedSource{Repository: repo, Revision: revision}
	parent := t.TempDir()
	destination := filepath.Join(parent, "snapshot")
	sourceFile(t, destination, "sentinel", "keep me")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tree, cleanup, err := source.Stage(ctx, parent)
	cleanup()
	if !errors.Is(err, context.Canceled) || tree != (SourceTree{}) {
		t.Fatalf("cancelled stage: %+v %v", tree, err)
	}
	tree, err = source.Snapshot(ctx, destination)
	if !errors.Is(err, context.Canceled) || tree != (SourceTree{}) {
		t.Fatalf("cancelled snapshot: %+v %v", tree, err)
	}
	tree, err = (PreviewSource{Worktree: repo}).Prepare(ctx)
	if !errors.Is(err, context.Canceled) || tree != (SourceTree{}) {
		t.Fatalf("cancelled worktree: %+v %v", tree, err)
	}
	assertSourceFile(t, destination, "sentinel", "keep me")
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 1 {
		t.Fatalf("cancelled preparation leaked staging: %v %v", entries, err)
	}
}

func TestSnapshotCacheRejectsSymlinkMarkersAndDirectories(t *testing.T) {
	for _, kind := range []string{"root", "directory", "marker"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			repo, revision := sourceRepo(t)
			destination := filepath.Join(t.TempDir(), "snapshot")
			outside := t.TempDir()
			sourceFile(t, outside, ".apptown/snapshot-sha", revision+"\n")
			sourceFile(t, outside, "index.html", "different game\n")
			link, target := destination, outside
			if kind != "root" {
				sourceFile(t, destination, "index.html", "stale game\n")
				link, target = filepath.Join(destination, ".apptown"), filepath.Join(outside, ".apptown")
			}
			if kind == "marker" {
				if err := os.Mkdir(link, 0o755); err != nil {
					t.Fatal(err)
				}
				link, target = filepath.Join(link, "snapshot-sha"), filepath.Join(target, "snapshot-sha")
			}
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			tree, err := (SelectedSource{Repository: repo, Revision: revision}).Snapshot(context.Background(), destination)
			if err != nil {
				t.Fatal(err)
			}
			assertSourceFile(t, tree.Directory, "index.html", "original game\n")
			assertSourceFile(t, outside, "index.html", "different game\n")
		})
	}
}

// StageCommit stages a resolved commit as Stage does: that commit's files
// (not a later one's, nor the folder's uncommitted edits) and its revision.
func TestStageCommitStagesWhatStageDoes(t *testing.T) {
	t.Parallel()
	repo, commit := sourceRepo(t)
	sourceFile(t, repo, "index.html", "later game\n")
	sourceCommit(t, repo)
	sourceFile(t, repo, "index.html", "uncommitted edits\n")
	ctx := context.Background()
	want, cleanWant, err := (SelectedSource{Repository: repo, Revision: commit}).Stage(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer cleanWant()
	got, cleanGot, err := StageCommit(ctx, repo, commit, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer cleanGot()
	if got.Revision != want.Revision {
		t.Fatalf("revision %q, Stage %q", got.Revision, want.Revision)
	}
	assertSourceFile(t, got.Directory, "index.html", "original game\n")
}
