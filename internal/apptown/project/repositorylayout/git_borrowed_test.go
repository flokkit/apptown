package repositorylayout

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/gitexec"
)

func borrowedFixture(t *testing.T) (string, string, string) {
	t.Helper()
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	hub, folder := filepath.Join(root, "hub"), filepath.Join(root, "workspace")
	if err := os.Mkdir(hub, 0o700); err != nil {
		t.Fatal(err)
	}
	gitCommand(t, hub, "init", "--quiet", "--template=", "-b", "main")
	gitCommand(t, hub, "config", "user.name", gitexec.FolderUserName)
	gitCommand(t, hub, "config", "user.email", gitexec.FolderUserEmail)
	gitCommand(t, hub, "remote", "add", "origin", "https://example.invalid/unchanged.git")
	put(t, filepath.Join(hub, "tracked"), "base\n")
	gitCommand(t, hub, "add", "tracked")
	gitCommand(t, hub, "commit", "--quiet", "-m", "base")
	gitCommand(t, hub, "repack", "-ad")
	gitCommand(t, hub, "prune-packed")
	gitCommand(t, root, "clone", "--shared", "--quiet", hub, folder)
	gitCommand(t, folder, "remote", "remove", "origin")
	gitCommand(t, folder, "config", "user.name", gitexec.FolderUserName)
	gitCommand(t, folder, "config", "user.email", gitexec.FolderUserEmail)
	return root, hub, folder
}
func TestBorrowedWorkspaceBecomesIndependentWithoutChangingWork(t *testing.T) {
	root, hub, folder := borrowedFixture(t)
	donorHead := gitCommand(t, hub, "rev-parse", "HEAD")
	put(t, filepath.Join(folder, "local"), "local-only history\n")
	gitCommand(t, folder, "add", "local")
	gitCommand(t, folder, "commit", "--quiet", "-m", "local")
	head := gitCommand(t, folder, "rev-parse", "HEAD")
	put(t, filepath.Join(folder, "tracked"), "staged\n")
	gitCommand(t, folder, "add", "tracked")
	put(t, filepath.Join(folder, "tracked"), "dirty\n")
	put(t, filepath.Join(folder, "untracked"), "keep\n")
	index, dirty := gitCommand(t, folder, "diff", "--cached", "--binary"), gitCommand(t, folder, "diff", "--binary")
	config, err := os.ReadFile(filepath.Join(folder, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	borrowed, found, err := InspectBorrowedWorkspace(folder, hub, root, root)
	if err != nil || !found {
		t.Fatalf("inventory: %+v %v %v", borrowed, found, err)
	}
	stage, backup := filepath.Join(root, "stage"), filepath.Join(root, "backup")
	if err := borrowed.StageStandalone(t.Context(), stage); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(backup, 0o700); err != nil {
		t.Fatal(err)
	}
	exchange, err := borrowed.PlanExchange(stage, backup)
	if err != nil {
		t.Fatal(err)
	}
	// Crash after preserving the original .git directory, before installing stage.
	if err := os.Rename(filepath.Join(folder, ".git"), filepath.Join(backup, "metadata")); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := exchange.Apply(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	packs, err := filepath.Glob(filepath.Join(hub, ".git", "objects", "pack", "*.pack"))
	if err != nil || len(packs) == 0 {
		t.Fatalf("packed fixture missing: %v", err)
	}
	a, err := os.Stat(packs[0])
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.Stat(filepath.Join(folder, ".git", "objects", "pack", filepath.Base(packs[0])))
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(a, b) {
		t.Fatal("materialized pack still hardlinks donor")
	}
	moved := hub + "-removed"
	if err := os.Rename(hub, moved); err != nil {
		t.Fatal(err)
	}
	if err := gitexec.CheckFolder(t.Context(), folder); err != nil {
		t.Fatal(err)
	}
	gitCommand(t, folder, "fsck", "--full", "--no-dangling")
	if gitCommand(t, folder, "rev-parse", "HEAD") != head || gitCommand(t, folder, "diff", "--cached", "--binary") != index || gitCommand(t, folder, "diff", "--binary") != dirty {
		t.Fatal("materialization changed HEAD/index/working files")
	}
	content(t, filepath.Join(folder, "untracked"), "keep\n")
	after, err := os.ReadFile(filepath.Join(folder, ".git", "config"))
	if err != nil || !bytes.Equal(after, config) {
		t.Fatal("workspace config changed")
	}
	if _, err := os.Stat(filepath.Join(backup, "metadata", "objects", "info", "alternates")); err != nil {
		t.Fatal("original alternate evidence lost", err)
	}
	gitCommand(t, folder, "commit", "--quiet", "-m", "staged changes survive")
	if gitCommand(t, moved, "rev-parse", "HEAD") != donorHead || gitCommand(t, moved, "remote", "get-url", "origin") != "https://example.invalid/unchanged.git" {
		t.Fatal("donor identity changed")
	}
}
func TestBorrowedWorkspaceResolvesRelativeAndRestoredAlternates(t *testing.T) {
	for _, relative := range []bool{false, true} {
		t.Run(map[bool]string{false: "absolute restore", true: "relative restore"}[relative], func(t *testing.T) {
			old, hub, folder := borrowedFixture(t)
			head := gitCommand(t, folder, "rev-parse", "HEAD")
			if relative {
				rel, err := filepath.Rel(filepath.Join(folder, ".git", "objects"), filepath.Join(hub, ".git", "objects"))
				if err != nil {
					t.Fatal(err)
				}
				put(t, filepath.Join(folder, ".git", "objects", "info", "alternates"), rel+"\n")
			}
			root := old + "-restored"
			if err := os.Rename(old, root); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.RemoveAll(root) })
			hub, folder = filepath.Join(root, "hub"), filepath.Join(root, "workspace")
			borrowed, found, err := InspectBorrowedWorkspace(folder, hub, old, root)
			if err != nil || !found {
				t.Fatalf("restore inventory: %v %v", found, err)
			}
			stage := filepath.Join(root, "stage")
			if err := borrowed.StageStandalone(t.Context(), stage); err != nil {
				t.Fatal(err)
			}
			if got := gitCommand(t, stage, "rev-parse", "HEAD"); got != head {
				t.Fatalf("restored HEAD: %s", got)
			}
		})
	}
}
func TestBorrowedWorkspaceRefusesUnownedAndAmbiguousDependencies(t *testing.T) {
	for _, kind := range []string{"foreign", "multiple", "quoted", "http", "nested", "missing", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			root, hub, folder := borrowedFixture(t)
			alternate := filepath.Join(folder, ".git", "objects", "info", "alternates")
			switch kind {
			case "foreign":
				other := filepath.Join(root, "other", ".git", "objects")
				if err := os.MkdirAll(other, 0o700); err != nil {
					t.Fatal(err)
				}
				put(t, alternate, other+"\n")
			case "multiple":
				raw, err := os.ReadFile(alternate)
				if err != nil {
					t.Fatal(err)
				}
				put(t, alternate, string(raw)+string(raw))
			case "quoted":
				put(t, alternate, "\""+filepath.Join(hub, ".git", "objects")+"\"\n")
			case "http":
				put(t, filepath.Join(folder, ".git", "objects", "info", "http-alternates"), "https://example.invalid/objects\n")
			case "nested":
				put(t, filepath.Join(hub, ".git", "objects", "info", "alternates"), "another-store\n")
			case "missing":
				if err := os.Rename(filepath.Join(hub, ".git", "objects"), filepath.Join(hub, ".git", "missing")); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(filepath.Join(hub, ".git", "objects"), filepath.Join(folder, ".git", "borrowed-link")); err != nil {
					t.Fatal(err)
				}
			}
			if _, _, err := InspectBorrowedWorkspace(folder, hub, root, root); err == nil {
				t.Fatal("unproved dependency accepted")
			}
		})
	}
}
func TestBorrowedMaterializationRefusesConfigAndObjectConflicts(t *testing.T) {
	for _, kind := range []string{"config", "object"} {
		t.Run(kind, func(t *testing.T) {
			root, hub, folder := borrowedFixture(t)
			if kind == "config" {
				gitCommand(t, folder, "config", "filter.untrusted.clean", "some-command")
			} else {
				packs, err := filepath.Glob(filepath.Join(hub, ".git", "objects", "pack", "*.pack"))
				if err != nil || len(packs) == 0 {
					t.Fatal("missing pack")
				}
				put(t, filepath.Join(folder, ".git", "objects", "pack", filepath.Base(packs[0])), "conflicting object contents")
			}
			borrowed, found, err := InspectBorrowedWorkspace(folder, hub, root, root)
			if err != nil || !found {
				t.Fatalf("inventory: %v %v", found, err)
			}
			if err := borrowed.StageStandalone(t.Context(), filepath.Join(root, "stage")); err == nil {
				t.Fatal("unsupported state silently changed")
			}
			if _, err := os.Stat(filepath.Join(folder, ".git", "objects", "info", "alternates")); err != nil {
				t.Fatal("original alternate removed on refusal", err)
			}
		})
	}
}
func TestBorrowedExchangeRefusesMixedAndChangedOriginalMetadata(t *testing.T) {
	root, hub, folder := borrowedFixture(t)
	borrowed, _, err := InspectBorrowedWorkspace(folder, hub, root, root)
	if err != nil {
		t.Fatal(err)
	}
	stage, backup := filepath.Join(root, "stage"), filepath.Join(root, "backup")
	if err := borrowed.StageStandalone(t.Context(), stage); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(backup, 0o700); err != nil {
		t.Fatal(err)
	}
	exchange, err := borrowed.PlanExchange(stage, backup)
	if err != nil {
		t.Fatal(err)
	}
	mixed := exchange
	mixed.Admin = filepath.Join(hub, ".git", "worktrees", "other")
	if err := mixed.Apply(t.Context()); err == nil || !strings.Contains(err.Error(), "mixed") {
		t.Fatalf("mixed exchange accepted: %v", err)
	}
	put(t, filepath.Join(folder, ".git", "unexpected"), "concurrent change")
	if err := exchange.Apply(t.Context()); err == nil {
		t.Fatal("changed original metadata overwritten")
	}
	if entries, err := os.ReadDir(backup); err != nil || len(entries) != 0 {
		t.Fatalf("changed source moved despite refusal: %v %v", entries, err)
	}
	if err := os.Remove(filepath.Join(folder, ".git", "unexpected")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(folder, ".git"), filepath.Join(backup, "metadata")); err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(folder, ".git", "foreign"), "do not overwrite")
	if err := exchange.Apply(t.Context()); err == nil {
		t.Fatal("unrelated metadata destination overwritten")
	}
	content(t, filepath.Join(folder, ".git", "foreign"), "do not overwrite")
	if _, err := os.Stat(filepath.Join(backup, "metadata", "HEAD")); err != nil {
		t.Fatal("original metadata backup lost", err)
	}
}

func TestBorrowedInventoryRefusesSourceChangedBeforeStaging(t *testing.T) {
	root, hub, folder := borrowedFixture(t)
	borrowed, _, err := InspectBorrowedWorkspace(folder, hub, root, root)
	if err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(folder, ".git", "changed"), "concurrent metadata")
	stage := filepath.Join(root, "stage")
	if err := borrowed.StageStandalone(t.Context(), stage); err == nil {
		t.Fatal("changed source accepted")
	}
	if _, err := os.Stat(stage); !os.IsNotExist(err) {
		t.Fatalf("staging began before ownership-change refusal: %v", err)
	}
}
