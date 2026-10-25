package repositoryedit

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"github.com/flokkit/apptown/internal/ids"
)

// A discard whose caller stops after the tracked files are restored either
// finishes (the untracked file gone) or is an error with no receipt: never
// a recorded discard of files still there.
//
// Kills: the clean on the stopped caller with its failure ignored.
func TestAStoppedDiscardIsNeverAFalseReceipt(t *testing.T) {
	t.Parallel()
	db, dataDir, id, _, untracked := driftFixture(t)
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

// driftFixture is a project whose repository has a tracked file edited and
// an untracked file added outside AppTown.
func driftFixture(t *testing.T) (db *database.DB, dataDir string, id ids.ID, tracked, untracked string) {
	t.Helper()
	db = databaseSuite.New(t)
	dataDir = t.TempDir()
	id, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "drift", Name: "Drift"})
	})
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(dataDir, "workspaces", "project-"+id.String())
	if err := scm.InitializeRepository(t.Context(), repo, "main"); err != nil {
		t.Fatal(err)
	}
	tracked, untracked = filepath.Join(repo, "tracked.txt"), filepath.Join(repo, "stray.txt")
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
	return db, dataDir, id, tracked, untracked
}

// A discard destroys edits: when its request cannot be recorded, nothing is
// discarded.
func TestADiscardWhoseRequestIsNotRecordedChangesNothing(t *testing.T) {
	t.Parallel()
	db, dataDir, id, tracked, untracked := driftFixture(t)
	editor := NewIntegrity(&scm.RepositoryLocks{}, execution.Runner{}, nil, db, dataDir, nil)
	p := &project.Project{ID: id, Slug: "drift", DefaultBranch: "main"}
	restore := db.FailNextStatement("-- name: AppendReceipt")
	_, err := editor.Resolve(t.Context(), p, "discard", "admin")
	restore()
	if err == nil {
		t.Fatal("a discard whose request was not recorded reported success")
	}
	if b, err := os.ReadFile(tracked); err != nil || string(b) != "edited outside\n" {
		t.Fatalf("the edited file was restored: %q %v", b, err)
	}
	if _, err := os.Stat(untracked); err != nil {
		t.Fatalf("the untracked file was removed: %v", err)
	}
	if _, err := editor.Resolve(t.Context(), p, "discard", "admin"); err != nil {
		t.Fatal(err)
	}
	got, err := audit.Recent(t.Context(), db, audit.Filter{ProjectID: id, KindPrefix: "integrity."})
	if err != nil || len(got) != 2 || got[0].Kind != "integrity.out-of-band-edits" || got[1].Kind != "integrity.discard-requested" {
		t.Fatalf("a discard's request and outcome: %+v %v", got, err)
	}
}
