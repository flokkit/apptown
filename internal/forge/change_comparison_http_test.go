package forge

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/gitexec/gittest"
	"flokkit.io/forge/internal/ids"
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

// A change only a landing ever worked (the ship workflow invoked on it to
// redeploy main) has no branch. Its Code tab says it has no code of its own
// and shows what its release put live that the release before it on that
// deployment did not, never a git error about a branch that never existed.
func TestChangePageShowsWhatAChangeWithNoCodeShipped(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	if err := a.Access.SeedAdministrator(context.Background(), "bls"); err != nil {
		t.Fatal(err)
	}
	pid, _, git := fleetRepo(t, a, "no-code", map[string]string{"app.txt": "first"})
	before := strings.TrimSpace(git("rev-parse", "HEAD"))
	writeRepoFile(t, a, "no-code", "app.txt", "second")
	git("commit", "-qam", "main moves on")
	after := strings.TrimSpace(git("rev-parse", "HEAD"))
	seedLibraryWorkflow(t, a, pid, "quiet-ship", `apiVersion: apptown.dev/v1
kind: Workflow
metadata: {name: quiet-ship}
spec:
  kind: pipeline
  role: landing
  workspace: project
  stages:
    - name: check
      steps:
        - {name: scan, uses: scan, with: {on: source}}
`)
	ch, err := a.Work.Create(a.asForge(context.Background(), pid, "test"), workflow.ChangeDraft{ProjectID: pid, Type: "chore", Title: "Redeploy main"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Work.StartRun(asLander(t, a), workflow.StartChangeRun{ChangeID: ch.ID, Workflow: "quiet-ship"}); err != nil {
		t.Fatal(err)
	}
	a.Work.Wait()
	// sql-guarantee: a ship of main on a change with no branch records no merge (live, 21 Oct); this fixture's quiet landing merges its empty branch, so its merge record is cleared to be that change.
	if _, err := a.DB.Exec(`UPDATE changes SET landed_sha='' WHERE id=$1`, ch.ID); err != nil {
		t.Fatal(err)
	}
	// Nor has such a change a branch: this fixture's landing made one.
	shipped, err := a.Work.Change(a.asForge(context.Background(), pid, "test"), ch.ID)
	if err != nil {
		t.Fatal(err)
	}
	git("branch", "-D", shipped.Branch)

	type view struct {
		CommittedDiff struct {
			Status, Message string
			Shipped         *struct{ Release, Previous, Since int64 }
		}
		Diffstat string
		Commits  []struct{ Subject string }
	}
	read := func() view {
		t.Helper()
		w := changeRequest(a, "bls", "GET", fmt.Sprintf("/api/v1/changes/%d", ch.ID), "")
		if w.Code != 200 {
			t.Fatalf("detail: %d %s", w.Code, w.Body.String())
		}
		var got view
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := read(); got.CommittedDiff.Status != "no-code" || !strings.Contains(got.CommittedDiff.Message, "no release carrying it is recorded as released") {
		t.Fatalf("before any release: %+v", got.CommittedDiff)
	}

	at := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	release := func(number int, commit string) ids.ID {
		at = at.Add(time.Minute)
		bundle, release := ids.New(at), ids.New(at.Add(time.Second))
		// sql-guarantee: TestChangePageShowsWhatAChangeWithNoCodeShipped needs two released releases built from known commits; a real release needs a target and a deploy, which this page's read does not depend on.
		if _, err := a.DB.Exec(`INSERT INTO release_bundles(id,project_id,contract_revision,contract_hash,parts_json,init_json,built_from_json,created_at) VALUES ($1,$2,$3,'','{}','{}','{}',$4)`, bundle, pid, commit, at); err != nil {
			t.Fatal(err)
		}
		// sql-guarantee: as above.
		if _, err := a.DB.Exec(`INSERT INTO releases(id,project_id,number,state,deployment,created_at,bundle_id) VALUES ($1,$2,$3,'released','main',$4,$5)`, release, pid, number, at, bundle); err != nil {
			t.Fatal(err)
		}
		return release
	}
	release(1, before)
	// sql-guarantee: as above; Start chooses membership from the shipped set, which this change is not in.
	if _, err := a.DB.Exec(`INSERT INTO release_changes(release_id,change_id) VALUES ($1,$2)`, release(2, after), ch.ID); err != nil {
		t.Fatal(err)
	}
	got := read()
	if got.CommittedDiff.Status != "no-code" || got.CommittedDiff.Shipped == nil || got.CommittedDiff.Shipped.Since != 1 ||
		!strings.Contains(got.CommittedDiff.Message, "shipped main as it stood at "+after[:7]) || got.Diffstat == "" ||
		len(got.Commits) != 1 || got.Commits[0].Subject != "main moves on" {
		t.Fatalf("after its release: %+v", got)
	}
	w := changeRequest(a, "bls", "GET", fmt.Sprintf("/api/v1/changes/%d/diff", ch.ID), "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "-first") || !strings.Contains(w.Body.String(), "+second") {
		t.Fatalf("diff: %d %s", w.Code, w.Body.String())
	}

	// A branch that exists holds the change's own commits, shown as such.
	git("branch", shipped.Branch, after)
	if got := read(); got.CommittedDiff.Status == "no-code" {
		t.Fatalf("a shipped change's own branch hidden: %+v", got.CommittedDiff)
	}
	git("branch", "-D", shipped.Branch)

	// Shipped (its landing run shipped it), its preview is what its release
	// shipped, not a folder it never had.
	src, err := previewDependenciesForTest(a).Sources.Change(a.asForge(context.Background(), pid, "test"), ch.ID)
	if err != nil || src.SourceRevision != after {
		t.Fatalf("preview source: %+v %v, want %s", src, err, after)
	}
}
