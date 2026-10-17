package repositoryedit

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/gitexec"
	"flokkit.io/forge/internal/gitexec/gittest"
	"flokkit.io/forge/internal/ids"
)

// A discard whose caller stops after the tracked files are restored either
// finishes (the untracked file gone) or is an error with no receipt: never
// a recorded discard of files still there.
//
// Kills: the clean on the stopped caller with its failure ignored.
func TestAStoppedDiscardIsNeverAFalseReceipt(t *testing.T) {
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
	tracked, untracked := filepath.Join(repo, "tracked.txt"), filepath.Join(repo, "stray.txt")
	if err := os.WriteFile(tracked, []byte("original\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, repo, "add", "tracked.txt")
	gittest.RunAsTester(t, repo, "commit", "-qm", "tracked")
	for path, body := range map[string]string{tracked: "edited outside\n", untracked: "left outside\n"} {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	editor := NewIntegrity(&scm.RepositoryLocks{}, execution.Runner{}, nil, db, dataDir, nil)
	stopped, stop := context.WithCancel(t.Context())
	editor.git = func(ctx context.Context, dir string, args ...string) (string, error) {
		out, err := gitexec.Run(ctx, dir, args...)
		if args[0] == "checkout" {
			stop() // the administrator disconnects once the tracked files are back
		}
		return out, err
	}
	p := &project.Project{ID: id, Slug: "drift", DefaultBranch: "main"}
	res, err := editor.Resolve(stopped, p, "discard", "admin")
	receipts, readErr := audit.Recent(t.Context(), db, audit.Filter{ProjectID: id, KindPrefix: "integrity.out-of-band-edits"})
	if readErr != nil {
		t.Fatal(readErr)
	}
	_, statErr := os.Stat(untracked)
	switch {
	case err == nil && (!res.OK || !os.IsNotExist(statErr) || len(receipts) != 1):
		t.Fatalf("a discard reported done: %+v; untracked file %v; receipts %d", res, statErr, len(receipts))
	case err != nil && len(receipts) != 0:
		t.Fatalf("a discard that failed (%v) recorded %d receipts", err, len(receipts))
	}
}
