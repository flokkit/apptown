package repositorylayout

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/flokkit/apptown/internal/gitexec"
)

func gitCommand(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitexec.HubRun(t.Context(), dir, args...)
	if err != nil {
		t.Fatalf("git %v: %s %v", args, out, err)
	}
	return out
}
func TestStagedLinkedMetadataKeepsDirtyIndexHistoryAndIndependentCommits(t *testing.T) {
	root := t.TempDir()
	hub := filepath.Join(root, "hub")
	folder := filepath.Join(root, "change")
	if err := os.Mkdir(hub, 0o700); err != nil {
		t.Fatal(err)
	}
	gitCommand(t, hub, "init", "--quiet", "--template=", "-b", "main")
	gitCommand(t, hub, "config", "user.name", gitexec.FolderUserName)
	gitCommand(t, hub, "config", "user.email", gitexec.FolderUserEmail)
	gitCommand(t, hub, "remote", "add", "origin", "https://example.invalid/team/original.git")
	put(t, filepath.Join(hub, "tracked"), "base\n")
	gitCommand(t, hub, "add", "tracked")
	gitCommand(t, hub, "commit", "--quiet", "-m", "base")
	base := gitCommand(t, hub, "rev-parse", "HEAD")
	gitCommand(t, hub, "worktree", "add", "--quiet", "-b", "change", folder)
	put(t, filepath.Join(folder, "tracked"), "staged\n")
	gitCommand(t, folder, "add", "tracked")
	put(t, filepath.Join(folder, "tracked"), "unstaged\n")
	put(t, filepath.Join(folder, "untracked"), "keep me")
	stagedBefore := gitCommand(t, folder, "diff", "--cached", "--binary")
	dirtyBefore := gitCommand(t, folder, "diff", "--binary")
	link, linked, err := inspectLinkedForTest(folder, hub)
	if err != nil || !linked {
		t.Fatalf("linked inventory: %+v %v %v", link, linked, err)
	}
	destination := filepath.Join(root, "prepared")
	if err := link.StageStandalone(t.Context(), destination); err != nil {
		t.Fatal(err)
	}
	// The staging component cannot change the original worktree or hub.
	if got := gitCommand(t, folder, "diff", "--cached", "--binary"); got != stagedBefore {
		t.Fatal("staging changed original index")
	}

	backup := filepath.Join(root, "metadata-backup")
	if err := os.Mkdir(backup, 0o700); err != nil {
		t.Fatal(err)
	}
	exchange, err := link.PlanExchange(destination, hub, backup)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a crash after preserving the pointer and before installing .git.
	if err := os.Rename(filepath.Join(folder, ".git"), filepath.Join(backup, "pointer")); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := exchange.Apply(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(backup, "admin", "HEAD")); err != nil {
		t.Fatal("original administration evidence lost", err)
	}

	if err := gitexec.CheckFolder(t.Context(), folder); err != nil {
		t.Fatal(err)
	}
	if gitCommand(t, folder, "rev-parse", "HEAD") != base || gitCommand(t, folder, "diff", "--cached", "--binary") != stagedBefore || gitCommand(t, folder, "diff", "--binary") != dirtyBefore {
		t.Fatal("conversion changed HEAD, staged index or dirty files")
	}
	content(t, filepath.Join(folder, "untracked"), "keep me")
	gitCommand(t, folder, "commit", "--quiet", "-m", "staged work survives")
	if gitCommand(t, folder, "rev-parse", "HEAD") == base {
		t.Fatal("standalone workspace could not commit")
	}
	if gitCommand(t, hub, "rev-parse", "HEAD") != base {
		t.Fatal("workspace commit moved hub main")
	}
	if got := gitCommand(t, hub, "remote", "get-url", "origin"); got != "https://example.invalid/team/original.git" {
		t.Fatalf("changed upstream: %q", got)
	}
}

func TestLinkedConversionRefusesBehavioralConfigBeforeStaging(t *testing.T) {
	folder, hub, _ := linkedPointers(t)
	put(t, filepath.Join(hub, ".git", "config"), "[filter \"custom\"]\n clean = some-program\n")
	link, linked, err := inspectLinkedForTest(folder, hub)
	if err != nil || !linked {
		t.Fatalf("inventory: %v %v", linked, err)
	}
	destination := filepath.Join(t.TempDir(), "prepared")
	if err := link.StageStandalone(t.Context(), destination); err == nil {
		t.Fatal("silently dropped behavior-changing Git config")
	}
	if _, err := os.Lstat(destination); !os.IsNotExist(err) {
		t.Fatalf("created staging before config refusal: %v", err)
	}
}

func TestRestoredLinkedWorkspaceNeverReadsOriginalTree(t *testing.T) {
	parent := t.TempDir()
	old := filepath.Join(parent, "original")
	if err := os.Mkdir(old, 0o700); err != nil {
		t.Fatal(err)
	}
	// Git writes canonical absolute pointers, as a real archived installation does.
	old, err := filepath.EvalSymlinks(old)
	if err != nil {
		t.Fatal(err)
	}
	hub := filepath.Join(old, "hub")
	folder := filepath.Join(old, "change")
	if err := os.Mkdir(hub, 0o700); err != nil {
		t.Fatal(err)
	}
	gitCommand(t, hub, "init", "--quiet", "--template=", "-b", "main")
	gitCommand(t, hub, "config", "user.name", gitexec.FolderUserName)
	gitCommand(t, hub, "config", "user.email", gitexec.FolderUserEmail)
	put(t, filepath.Join(hub, "file"), "base")
	gitCommand(t, hub, "add", "file")
	gitCommand(t, hub, "commit", "--quiet", "-m", "base")
	gitCommand(t, hub, "worktree", "add", "--quiet", "-b", "work", folder)
	put(t, filepath.Join(folder, "file"), "staged")
	gitCommand(t, folder, "add", "file")
	index := gitCommand(t, folder, "diff", "--cached", "--binary")
	root := filepath.Join(filepath.Dir(old), "restored")
	if err := os.Rename(old, root); err != nil {
		t.Fatal(err)
	}
	hub, folder = filepath.Join(root, "hub"), filepath.Join(root, "change")
	link, linked, err := InspectRestoredLinkedWorkspace(folder, hub, old, root)
	if err != nil || !linked {
		t.Fatalf("restored ownership: %+v %v %v", link, linked, err)
	}
	stage := filepath.Join(root, "stage")
	if err := link.StageStandalone(t.Context(), stage); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(folder, ".git"), filepath.Join(root, "saved-pointer")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(stage, ".git"), filepath.Join(folder, ".git")); err != nil {
		t.Fatal(err)
	}
	if got := gitCommand(t, folder, "diff", "--cached", "--binary"); got != index {
		t.Fatal("restored index changed")
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("old tree unexpectedly exists: %v", err)
	}
}
