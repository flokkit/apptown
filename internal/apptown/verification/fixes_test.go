package verification

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/strs"
)

// A verdict that flips to conforming by a commit no shipped Change landed is
// still a fix: its attribution names no Change and keeps the commit range
// between the two judged commits, so the record says where the fix came from.
func TestAFixNoChangeLandedKeepsItsCommitRange(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	pid, err := database.Transact(t.Context(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "fixed", Name: "Fixed"})
	})
	if err != nil {
		t.Fatal(err)
	}
	dataDir := t.TempDir()
	shas := gittest.Repository(t, filepath.Join(dataDir, "workspaces", "project-fixed"), "main",
		gittest.Commit{Message: "failing", Files: map[string]string{"a.txt": "FIXME"}},
		gittest.Commit{Message: "fixed directly", Files: map[string]string{"a.txt": "clean"}})
	fleet := Fleet{db: db, dataDir: dataDir}
	fix, err := fleet.prepareVerdictFix(t.Context(),
		conformance.Verdict{ProjectID: pid, SHA: shas[0], State: conformance.DoesNotConform},
		conformance.Verdict{ProjectID: pid, SHA: shas[1], State: conformance.Conforms})
	if err != nil {
		t.Fatal(err)
	}
	if want := strs.ShortSHA(shas[0]) + ".." + strs.ShortSHA(shas[1]); fix == nil || len(fix.Changes) != 0 || fix.Commits != want {
		t.Fatalf("a fix no Change landed: %+v, want no changes and commits %q", fix, want)
	}
}
