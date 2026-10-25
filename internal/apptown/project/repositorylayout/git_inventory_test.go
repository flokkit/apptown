package repositorylayout

import (
	"os"
	"path/filepath"
	"testing"
)

func linkedPointers(t *testing.T) (folder, hub, admin string) {
	t.Helper()
	root := t.TempDir()
	folder = filepath.Join(root, "change")
	hub = filepath.Join(root, "hub")
	admin = filepath.Join(hub, ".git", "worktrees", "change")
	put(t, filepath.Join(folder, ".git"), "gitdir: "+admin+"\n")
	put(t, filepath.Join(admin, "gitdir"), filepath.Join(folder, ".git")+"\n")
	put(t, filepath.Join(admin, "commondir"), "../..\n")
	return
}
func TestLinkedWorkspaceInventoryVerifiesBothOwnershipDirections(t *testing.T) {
	folder, hub, _ := linkedPointers(t)
	got, linked, err := inspectLinkedForTest(folder, hub)
	if err != nil || !linked || got.Admin != filepath.Join("worktrees", "change") {
		t.Fatalf("consistent pointer refused: %+v %v %v", got, linked, err)
	}
	if _, _, err := inspectLinkedForTest(folder, filepath.Join(t.TempDir(), "other-hub")); err == nil {
		t.Fatal("adopted another project's Git metadata")
	}
}
func TestLinkedWorkspaceInventoryRefusesChangedOrExternalMetadata(t *testing.T) {
	for _, kind := range []string{"backlink", "commondir", "locked", "config", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			folder, hub, admin := linkedPointers(t)
			switch kind {
			case "backlink":
				put(t, filepath.Join(admin, "gitdir"), "/another/workspace/.git\n")
			case "commondir":
				put(t, filepath.Join(admin, "commondir"), "/another/hub/.git\n")
			case "locked":
				put(t, filepath.Join(admin, "locked"), "keep this workspace")
			case "config":
				put(t, filepath.Join(admin, "config.worktree"), "[core]\n bare = false\n")
			case "symlink":
				if err := os.Remove(filepath.Join(admin, "gitdir")); err != nil {
					t.Fatal(err)
				}
				target := filepath.Join(t.TempDir(), "pointer")
				put(t, target, filepath.Join(folder, ".git"))
				if err := os.Symlink(target, filepath.Join(admin, "gitdir")); err != nil {
					t.Fatal(err)
				}
			}
			if _, _, err := inspectLinkedForTest(folder, hub); err == nil {
				t.Fatal("accepted ambiguous linked workspace")
			}
		})
	}
}

func inspectLinkedForTest(folder, hub string) (LinkedWorkspace, bool, error) {
	return inspectLinkedWorkspace(folder, hub, "", "")
}
