package apptown

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestChangeSourceSettlesWithMergeReceipt(t *testing.T) {
	t.Parallel()
	a, source := mergeSettlementFixture(t)
	sha := gittest.Run(t, requireLocation(t)(source.Repository(a.cfg.dataDir)), "rev-parse", "HEAD")
	runID, changeID := source.RunID, source.ChangeID
	pendingMergeForTest(t, a, runID, sha)
	ofRun := func(e workflow.Event) bool { return e.RunID == runID }
	beforeEvents := workEventCount(t, a, ofRun)
	completionExec(t, a.DB, `ALTER TABLE changes ADD CONSTRAINT reject_test_landed CHECK (landed_sha != 'rejected')`)
	service := scmFor(a)
	ctx := workflow.SCMInspectionContext(context.Background(), a.DB)
	if _, err := service.Inspect(ctx, runID, provenMergeDriver{result: "rejected"}); err == nil {
		t.Fatal("source persistence failure was hidden")
	}
	requireMergeRecord(t, a, runID, scm.Pending, "")
	requireEvents(t, a, beforeEvents, ofRun)
	if _, err := service.Inspect(ctx, runID, provenMergeDriver{result: sha}); err != nil {
		t.Fatal(err)
	}
	ch, err := a.changeByID(ctx, changeID)
	if err != nil || ch.LandedSHA != sha {
		t.Fatalf("source not recorded: %+v %v", ch, err)
	}
	requireMergeRecord(t, a, runID, scm.Recorded, sha)
	requireEvents(t, a, beforeEvents+1, ofRun)
	requireEvents(t, a, 1, func(e workflow.Event) bool {
		return e.RunID == runID && e.Kind == "merge.completed" && payloadField(e, "sha") == sha
	})
}

func TestChangePreviewUsesPreparedHistoricalSource(t *testing.T) {
	t.Parallel()
	a, _ := bootWorld(t)
	repo := a.projectRepoPath("p")
	writeBootFixture(t, repo)
	if err := os.WriteFile(filepath.Join(repo, "index.html"), []byte("original game\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, repo, "init", "-q")
	gittest.Run(t, repo, "add", ".")
	gittest.Run(t, repo, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-qm", "historical game")
	original := gittest.Run(t, repo, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(repo, ".apptown/app.yaml"), []byte("invalid: current tree must not supply the contract\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, repo, "add", ".")
	gittest.Run(t, repo, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-qm", "later source")
	// This is the selected Workflow projection supplied to preview preparation.
	ch := &workflow.Change{ID: idstest.N(7), ProjectID: idstest.N(1), State: workflow.ChangeShipped, LandedSHA: original}
	out, err := a.Preview.RehearseChange(context.Background(), previewChange(ch), false)
	if err != nil || out.URL == "" || out.Revision != original {
		t.Fatalf("historical preview: %+v %v", out, err)
	}

	body, err := os.ReadFile(filepath.Join(a.cfg.dataDir, "workspaces", "asof-change-00000000-0007-7000-8000-000000000000", original, "index.html"))
	if err != nil || string(body) != "original game\n" {
		t.Fatalf("selected source: %q %v", body, err)
	}
}
