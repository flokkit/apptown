package forge

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/gitexec/gittest"
)

func TestChangePageDistinguishesEmptyCommittedDiffDirtyWorkAndUnreadableBranch(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	if err := a.Access.SeedAdministrator(context.Background(), "bls"); err != nil {
		t.Fatal(err)
	}
	pid, _, git := fleetRepo(t, a, "comparison", map[string]string{"app.txt": "original"})
	ch := engineChange(t, a, a.projectByID(context.Background(), pid))
	ws := filepath.Join(a.cfg.dataDir, "workspaces", fmt.Sprintf("change-%d", ch.ID))
	if err := os.WriteFile(filepath.Join(ws, "work.txt"), []byte("in progress"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Other work on main must not become this change's diff.
	writeRepoFile(t, a, "comparison", "other.txt", "other work")
	git("add", ".")
	git("commit", "-qm", "main advances")
	read := func() (string, string, int) {
		t.Helper()
		w := changeRequest(a, "bls", "GET", fmt.Sprintf("/api/v1/changes/%d", ch.ID), "")
		if w.Code != 200 {
			t.Fatalf("detail: %d %s", w.Code, w.Body.String())
		}
		var got struct {
			CommittedDiff struct{ Status, Message string }
			Editing       int
		}
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		return got.CommittedDiff.Status, got.CommittedDiff.Message, got.Editing
	}
	status, message, editing := read()
	if status != "empty" || message != workflow.EmptyCommittedChange || editing != 1 {
		t.Fatalf("empty committed/dirty workspace: %s %s edits=%d", status, message, editing)
	}
	gittest.Run(t, ws, "add", "work.txt")
	gittest.Run(t, ws, "commit", "-qm", "real change")
	status, _, editing = read()
	if status != "changed" || editing != 0 {
		t.Fatalf("committed workspace not observed: %s edits=%d", status, editing)
	}
	// Commit and revert still leave history but a net empty diff.
	gittest.Run(t, ws, "revert", "--no-edit", "HEAD")
	status, message, _ = read()
	if status != "empty" || message != workflow.EmptyCommittedChange {
		t.Fatalf("net empty diff: %s %s", status, message)
	}
	if err := os.RemoveAll(ws); err != nil {
		t.Fatal(err)
	}
	status, message, _ = read()
	if status != "unavailable" || message == workflow.EmptyCommittedChange {
		t.Fatalf("missing branch claimed empty: %s %s", status, message)
	}
	w := changeRequest(a, "bls", "GET", fmt.Sprintf("/api/v1/changes/%d/diff", ch.ID), "")
	if w.Code != 404 {
		t.Fatalf("missing branch diff: %d %s", w.Code, w.Body.String())
	}
}
