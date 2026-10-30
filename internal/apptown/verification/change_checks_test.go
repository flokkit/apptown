package verification

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
)

// A change is checked against the revision its own run reads. When that run
// or its recorded adoption cannot be read, the check fails: the project's
// current adoption is another revision's law, not a stand-in. A change with
// no run reads the project's current receipt, and a project that has
// adopted nothing has no checks.
//
// Kills: the fall-through from an unreadable run to the current adoption
// (fallback audit, verification/declarations.go).
func TestAChangesChecksAreItsRunsRevisionOrAnError(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	create := func(slug string, revision int64) ids.ID {
		t.Helper()
		pid, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
			id, err := project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: slug, Name: slug})
			if err != nil {
				return id, err
			}
			return id, adoption.InitializeIn(t.Context(), tx, id, revision, "")
		})
		if err != nil {
			t.Fatal(err)
		}
		return pid
	}
	pid := create("snapshot", 0)
	changes := NewChanges(ChangeDependencies{DB: db, DataDir: t.TempDir()})
	// No such run: as a run whose read fails.
	gone := &workflow.Change{ID: idstest.N(1), ProjectID: pid, RunID: idstest.N(99)}
	if checks, ad, err := changes.changeChecks(t.Context(), gone); err == nil {
		t.Fatalf("an unreadable run gave checks %v from %+v", checks, ad)
	}
	runless := &workflow.Change{ID: idstest.N(2), ProjectID: pid}
	if checks, ad, err := changes.changeChecks(t.Context(), runless); err != nil || checks != nil || ad != nil {
		t.Fatalf("a project with no adoption: %v, %+v, %v; want no checks", checks, ad, err)
	}
	// A current revision whose receipt is missing is unknown, not "nothing
	// adopted" (InitializeIn records the revision without its receipt).
	lost := &workflow.Change{ID: idstest.N(3), ProjectID: create("lost-receipt", 3)}
	if checks, _, err := changes.changeChecks(t.Context(), lost); !errors.Is(err, adoption.ErrRevisionUnavailable) {
		t.Fatalf("a revision without its receipt: %v, %v; want the revision unavailable", checks, err)
	}
}
