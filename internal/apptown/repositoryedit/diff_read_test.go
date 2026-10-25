package repositoryedit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// A change's diff that Git cannot produce is an error, never an empty
// diff: a handoff or review reading it must not see "no difference".
//
// Kills: the discarded `git diff` error in Contributions.Diff (fallback
// audit F07).
func TestAChangeDiffGitCannotReadIsAnError(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	hub := filepath.Join(dataDir, "workspaces", "project-p")
	if err := os.MkdirAll(hub, 0o755); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, hub, "init", "-q", "-b", "main")
	gittest.RunAsTester(t, hub, "commit", "-q", "--allow-empty", "-m", "base")
	base := strings.TrimSpace(gittest.Run(t, hub, "rev-parse", "HEAD"))
	gittest.Run(t, hub, "checkout", "-q", "-b", "change")
	if err := os.WriteFile(filepath.Join(hub, "fix.go"), []byte("package fix\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, hub, "add", "fix.go")
	gittest.RunAsTester(t, hub, "commit", "-q", "-m", "fix")
	p := &project.Project{ID: idstest.N(2), Slug: "p", DefaultBranch: "main"}
	ch := &workflow.Change{ID: idstest.N(1), ProjectID: idstest.N(2), Branch: "change", BaseSHA: base}
	if patch, err := changePatch(t.Context(), dataDir, p, ch); err != nil || !strings.Contains(patch, "+package fix") {
		t.Fatalf("a readable diff: %q %v", patch, err)
	}
	// The change's file is gone from the object store: its commit and tree
	// still resolve, its contents cannot be read.
	blob := strings.TrimSpace(gittest.Run(t, hub, "rev-parse", "change:fix.go"))
	if err := os.Remove(filepath.Join(hub, ".git", "objects", blob[:2], blob[2:])); err != nil {
		t.Fatal(err)
	}
	if patch, err := changePatch(t.Context(), dataDir, p, ch); err == nil {
		t.Fatalf("an unreadable diff was returned as %q", patch)
	}
}

// An administrator's adopt or discard records the diff it decided over; a
// diff Git cannot read stops the decision before anything is kept or
// thrown away.
//
// Kills: the discarded diff read in Integrity.Resolve (fallback audit F07).
func TestAnUnreadableEditDiffStopsTheDecision(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	dataDir := t.TempDir()
	id, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "drift", Name: "Drift"})
	})
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(dataDir, "workspaces", "project-drift")
	if err := scm.InitializeRepository(t.Context(), repo, "main"); err != nil {
		t.Fatal(err)
	}
	tracked := filepath.Join(repo, "tracked.txt")
	if err := os.WriteFile(tracked, []byte("original\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, repo, "add", "tracked.txt")
	gittest.RunAsTester(t, repo, "commit", "-qm", "tracked")
	head := strings.TrimSpace(gittest.Run(t, repo, "rev-parse", "HEAD"))
	if err := os.WriteFile(tracked, []byte("edited outside\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	editor := NewIntegrity(&scm.RepositoryLocks{}, execution.Runner{}, nil, db, dataDir, nil)
	editor.git = func(ctx context.Context, dir string, args ...string) (string, error) {
		if args[0] == "diff" {
			return "", errors.New("git diff failed")
		}
		return gitexec.Run(ctx, dir, args...)
	}
	p := &project.Project{ID: id, Slug: "drift", DefaultBranch: "main"}
	for _, action := range []string{"discard", "adopt"} {
		if res, err := editor.Resolve(t.Context(), p, action, "admin"); err == nil {
			t.Fatalf("%s with an unreadable diff: %+v", action, res)
		}
	}
	receipts, err := audit.Recent(t.Context(), db, audit.Filter{ProjectID: id, KindPrefix: "integrity.out-of-band-edits"})
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(tracked)
	if err != nil {
		t.Fatal(err)
	}
	if now := strings.TrimSpace(gittest.Run(t, repo, "rev-parse", "HEAD")); now != head || string(body) != "edited outside\n" || len(receipts) != 0 {
		t.Fatalf("an unreadable diff still decided: head %s (was %s), file %q, %d receipts", now, head, body, len(receipts))
	}
}
