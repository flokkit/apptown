package repositoryedit

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/dbtest"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
)

// Adoption already committed the edits when its receipt is written. A failed
// receipt must report that retained effect, not success or a fictitious rollback.
func TestAdoptionReportsKeptEditsWhenItsReceiptFails(t *testing.T) {
	for _, failReceipt := range []bool{false, true} {
		name := "recorded"
		if failReceipt {
			name = "receipt fails"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			db, dataDir, id, tracked, untracked := driftFixture(t)
			repo := filepath.Dir(tracked)
			before := strings.TrimSpace(gittest.Run(t, repo, "rev-parse", "HEAD"))
			editor := NewIntegrity(&scm.RepositoryLocks{}, execution.Runner{}, noMirror{}, db, dataDir, nil)
			p := &project.Project{ID: id, Slug: "drift", DefaultBranch: "main"}
			if failReceipt {
				t.Cleanup(dbtest.NextStatementFailure(db, "-- name: AppendReceipt"))
			}
			result, err := editor.Resolve(t.Context(), p, "adopt", "deciding-admin")
			if failReceipt {
				if err == nil || result.OK || !strings.Contains(err.Error(), "kept the edits, but their receipt was not recorded") {
					t.Fatalf("unrecorded adoption reported incorrectly: result=%+v err=%v", result, err)
				}
			} else if err != nil || !result.OK || result.Action != "adopt" {
				t.Fatalf("recorded adoption: %+v %v", result, err)
			}
			after := strings.TrimSpace(gittest.Run(t, repo, "rev-parse", "HEAD"))
			if after == before {
				t.Fatal("adoption did not retain a new commit")
			}
			if message := gittest.Run(t, repo, "log", "-1", "--format=%B"); !strings.Contains(message, "decided by deciding-admin") {
				t.Fatalf("commit lost deciding actor: %q", message)
			}
			for path, want := range map[string]string{tracked: "edited outside", untracked: "left outside"} {
				if body := strings.TrimSpace(gittest.Run(t, repo, "show", "HEAD:"+filepath.Base(path))); body != want {
					t.Fatalf("kept file %s: %q", path, body)
				}
			}
			if status := gittest.Run(t, repo, "status", "--porcelain"); strings.TrimSpace(status) != "" {
				t.Fatalf("adopted edits remain uncommitted: %q", status)
			}
			receipts, readErr := audit.Recent(t.Context(), db, audit.Filter{ProjectID: id, KindPrefix: "integrity.out-of-band-edits"})
			if readErr != nil {
				t.Fatal(readErr)
			}
			if failReceipt {
				if len(receipts) != 0 {
					t.Fatalf("failed receipt was reported present: %+v", receipts)
				}
			} else if len(receipts) != 1 || receipts[0].Actor != "deciding-admin" || receipts[0].Subject != "adopt" {
				t.Fatalf("recorded adoption receipt: %+v", receipts)
			}
		})
	}
}
