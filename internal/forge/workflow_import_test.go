package forge

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/gitexec"
)

// Importing a branch: real work becomes a Change with provenance; empty
// branches and non-repo projects are refused with reasons.
func TestImportBranch(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	testAdministrator(t, a, "bls")
	plainTestProject(t, a.DB, "p", "P")
	p, _ := a.Projects.BySlug(context.Background(), "p")
	if _, err := a.Work.Import(verifiedContext(context.Background(), access.Principal{Kind: "user", Name: "bls", Via: "open"}), workflow.ImportBranch{ProjectID: p.ID, Branch: "anything", Title: ""}); err == nil {
		t.Fatal("non-repo project must refuse imports")
	}
	if err := a.lifecycle.InitializeRepository(t.Context(), &p, "", ""); err != nil {
		t.Fatal(err)
	}
	repo := a.projectRepoPath("p")

	// A branch with no commits beyond main is nothing to import.
	if _, err := gitexec.Run(t.Context(), repo, "branch", "empty-branch"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Work.Import(verifiedContext(context.Background(), access.Principal{Kind: "user", Name: "bls", Via: "open"}), workflow.ImportBranch{ProjectID: p.ID, Branch: "empty-branch", Title: ""}); err == nil || !strings.Contains(err.Error(), "nothing to import") {
		t.Fatalf("empty branch: %v", err)
	}
	if _, err := a.Work.Import(verifiedContext(context.Background(), access.Principal{Kind: "user", Name: "bls", Via: "open"}), workflow.ImportBranch{ProjectID: p.ID, Branch: "no-such-branch", Title: ""}); err == nil {
		t.Fatal("missing branch must refuse")
	}

	// Real work imports: change row with provenance, worktree on the branch.
	if _, err := gitexec.Run(t.Context(), repo, "checkout", "-b", "hand-work"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "work.txt"), []byte("done by hand\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stageAll(repo)
	if _, err := gitexec.Run(t.Context(), repo, "commit", "-m", "hand work"); err != nil {
		t.Fatal(err)
	}
	if _, err := gitexec.Run(t.Context(), repo, "checkout", "main"); err != nil {
		t.Fatal(err)
	}
	// Exercise the real transport handoff for this successful import. The owner
	// tests retain independent provenance and rollback checks.
	h := a.Handler()
	if w := authzCall(h, "outsider", "POST", "/api/v1/projects/p/import", `{"branch":"hand-work"}`); w.Code != 403 {
		t.Fatalf("foreign import: %d %s", w.Code, w.Body.String())
	}
	if w := authzCall(h, "bls", "POST", "/api/v1/projects/p/import", `{"branch":false}`); w.Code != 400 {
		t.Fatalf("invalid import: %d %s", w.Code, w.Body.String())
	}
	w := authzCall(h, "bls", "POST", "/api/v1/projects/p/import", `{"branch":"hand-work","title":"Imported work"}`)
	var out struct{ Change workflow.Change }
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 || out.Change.ID.IsZero() || out.Change.ProjectID != p.ID {
		t.Fatalf("import receipt: %d %s %v", w.Code, w.Body.String(), err)
	}
	ch := &out.Change
	if ch.Provenance != "imported/branch" || ch.Title != "Imported work" {
		t.Fatalf("import provenance: %+v", ch)
	}
	wt := filepath.Join(a.cfg.dataDir, "workspaces", fmt.Sprintf("change-%d", ch.ID))
	if _, err := os.Stat(filepath.Join(wt, "work.txt")); err != nil {
		t.Fatal("imported worktree must hold the branch's work")
	}
}
