package datalayout

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/project/repositorylayout"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestJournalRestagesIncompleteMetadataAndResumesAfterRepositoryMove(t *testing.T) {
	root := t.TempDir()
	mapping := repositorylayout.Mapping{ID: idstest.N(1), Slug: "original"}
	hub := mapping.Source(root)
	folder := filepath.Join(root, "workspaces", "change-"+idstest.N(2).String())
	if err := os.MkdirAll(hub, 0o700); err != nil {
		t.Fatal(err)
	}
	git := func(dir string, args ...string) string {
		t.Helper()
		out, err := gitexec.HubRun(t.Context(), dir, args...)
		if err != nil {
			t.Fatalf("git %v: %s %v", args, out, err)
		}
		return out
	}
	git(hub, "init", "--quiet", "--template=", "-b", "main")
	git(hub, "config", "user.name", gitexec.FolderUserName)
	git(hub, "config", "user.email", gitexec.FolderUserEmail)
	if err := os.WriteFile(filepath.Join(hub, "file"), []byte("base"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(hub, "add", "file")
	git(hub, "commit", "--quiet", "-m", "base")
	main := git(hub, "rev-parse", "HEAD")
	git(hub, "worktree", "add", "--quiet", "-b", "work", folder)
	if err := os.WriteFile(filepath.Join(folder, "file"), []byte("staged"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(folder, "add", "file")
	staged := git(folder, "diff", "--cached", "--binary")
	linked, ok, err := repositorylayout.InspectRestoredLinkedWorkspace(folder, hub, root, root)
	if err != nil || !ok {
		t.Fatalf("linked: %v %v", ok, err)
	}
	j := journal{Nonce: strings.Repeat("A", 26), Format: 1, Instance: "instance", Root: root, OriginalRoot: root, FromVersion: 1, Phase: "staging", Inventory: inventory{Mappings: []repositorylayout.Mapping{mapping}, References: []repositorylayout.Reference{mapping.Reference(root, root)}, Linked: []repositorylayout.LinkedWorkspace{linked}, Mains: []mainEvidence{{ProjectID: mapping.ID, Branch: "main", SHA: main}}}}
	if err := j.save(); err != nil {
		t.Fatal(err)
	}
	partial := filepath.Join(root, metadataName, j.Nonce, "0", "stage")
	if err := os.MkdirAll(partial, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(partial, "incomplete"), []byte("discard owned scratch"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := j.stageMetadata(t.Context()); err != nil {
		t.Fatal(err)
	}
	j, _, err = readJournal(root, "instance")
	if err != nil {
		t.Fatal(err)
	}
	if err := j.validatePlan(); err != nil {
		t.Fatal(err)
	}
	if len(j.Exchanges) != 1 || j.Phase != "moving" {
		t.Fatalf("staged journal: %+v", j)
	}
	if _, err := os.Stat(filepath.Join(partial, "incomplete")); !os.IsNotExist(err) {
		t.Fatalf("incomplete stage retained: %v", err)
	}
	moves, err := repositorylayout.Prepare(root, j.Instance, j.Inventory.Mappings)
	if err != nil {
		t.Fatal(err)
	}
	if err := moves.Install(); err != nil {
		t.Fatal(err)
	}
	if err := j.advance("metadata"); err != nil {
		t.Fatal(err)
	}
	// Restart after hubs move: apply metadata without replaying old-tree digests.
	j, _, err = readJournal(root, "instance")
	if err != nil {
		t.Fatal(err)
	}
	if err := j.validatePlan(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := j.Exchanges[0].Apply(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if err := j.verify(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := git(folder, "diff", "--cached", "--binary"); got != staged {
		t.Fatal("staged index changed across interrupted transition")
	}
	if _, err := os.Lstat(filepath.Join(root, "layout")); !os.IsNotExist(err) {
		t.Fatalf("component execution stamped layout before coordinator verification: %v", err)
	}
}
