package adoption

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/adoption/internal/adoptionsql"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/dbschema"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
)

var databaseSuite = dbtest.NewSuite(dbschema.Open)

func TestMain(m *testing.M)            { os.Exit(databaseSuite.Run(m)) }
func testDB(t *testing.T) *database.DB { t.Helper(); return databaseSuite.New(t) }

// testAccounts records each name as an account through the identity owner:
// adoption decides only for a person with an enabled account.
func testAccounts(t *testing.T, db *database.DB, names ...string) {
	t.Helper()
	ids := identity.New(db, identity.Config{})
	for _, name := range names {
		if _, err := ids.EnsureUser(context.Background(), name); err != nil {
			t.Fatal(err)
		}
	}
}

func adoptionTestProject(t *testing.T, db *database.DB, slug, name string) ids.ID {
	t.Helper()
	id, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: slug, Name: name})
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// The readers below are the owner's own reads of each record a project's
// adoption writes, so assertions see what the product sees.

// currentOf is the project's current aggregate; absence fails the test.
func currentOf(t *testing.T, db *database.DB, pid ids.ID) Adoption {
	t.Helper()
	a, err := CurrentIn(context.Background(), db, pid)
	if err != nil {
		t.Fatalf("current adoption of %d: %v", pid, err)
	}
	return *a
}

// noCurrent asserts the project has no current aggregate at all.
func noCurrent(t *testing.T, db *database.DB, pid ids.ID) {
	t.Helper()
	if a, err := CurrentIn(context.Background(), db, pid); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("current adoption of %d exists: %+v %v", pid, a, err)
	}
}

// revisionsOf is every recorded revision (edits and targets) of the project.
func revisionsOf(t *testing.T, db *database.DB, pid ids.ID) []RecordedRevision {
	t.Helper()
	const page = 1000
	h, err := newAdoptions(db).History(t.Context(), pid, page)
	if err != nil || len(h) == page {
		t.Fatalf("revisions of %d: %d %v", pid, len(h), err)
	}
	return h
}

// revisionsAt counts the project's recorded revisions with that number.
func revisionsAt(t *testing.T, db *database.DB, pid ids.ID, revision int64) int {
	t.Helper()
	n := 0
	for _, r := range revisionsOf(t, db, pid) {
		if r.Revision == revision {
			n++
		}
	}
	return n
}

func importsOf(t *testing.T, db *database.DB, pid ids.ID) []Import {
	t.Helper()
	im, err := importsIn(t.Context(), db, pid)
	if err != nil {
		t.Fatal(err)
	}
	return im
}

func packsOf(t *testing.T, db *database.DB, pid ids.ID) []ProjectPackRecord {
	t.Helper()
	p, err := ProjectPacksIn(t.Context(), db, pid)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func policiesOf(t *testing.T, db *database.DB, pid ids.ID) []Policy {
	t.Helper()
	p, err := ReadProjectPolicies(context.Background(), db, pid)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func extensionsOf(t *testing.T, db *database.DB, pid ids.ID) []string {
	t.Helper()
	e, err := enabledExtensionsIn(t.Context(), db, pid)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// laneRow is the project's selection row for a workflow: whether one exists
// at all (a deselected lane keeps its row), and whether it is enabled.
func laneRow(t *testing.T, db *database.DB, pid ids.ID, name string) (found, enabled bool) {
	t.Helper()
	r, err := adoptionsql.New(db.SQL()).ProjectLane(context.Background(), adoptionsql.ProjectLaneParams{ProjectID: pid, Name: name})
	found, err = database.Lookup(err)
	if err != nil {
		t.Fatal(err)
	}
	return found, found && r.Enabled
}

// ledger is every receipt whose kind starts with prefix, read through the
// audit owner; project 0 reads every project's lines. The read is bounded,
// so a full page cannot prove a total.
func ledger(t *testing.T, db *database.DB, pid ids.ID, prefix string) []audit.Entry {
	t.Helper()
	entries, err := audit.Recent(context.Background(), db, audit.Filter{ProjectID: pid, KindPrefix: prefix, Limit: audit.MaxLimit})
	if err != nil || len(entries) == audit.MaxLimit {
		t.Fatalf("ledger: %d %v", len(entries), err)
	}
	out := entries[:0]
	for _, e := range entries {
		// A project's reading includes the instance's lines; keep only its own.
		if (pid.IsZero() || e.ProjectID == pid) && strings.HasPrefix(e.Kind, prefix) {
			out = append(out, e)
		}
	}
	return out
}

// countOf counts the records that match.
func countOf[T any](records []T, match func(T) bool) int {
	n := 0
	for _, r := range records {
		if match(r) {
			n++
		}
	}
	return n
}

// receiptsWhere counts the receipts of exactly kind that match.
func receiptsWhere(t *testing.T, db *database.DB, pid ids.ID, kind string, match func(audit.Entry) bool) int {
	t.Helper()
	n := 0
	for _, e := range ledger(t, db, pid, kind) {
		if e.Kind == kind && (match == nil || match(e)) {
			n++
		}
	}
	return n
}

// targetsOf counts a project's recorded uplift targets for one change.
func targetsOf(t *testing.T, db *database.DB, pid, change ids.ID) int {
	t.Helper()
	n := 0
	for _, r := range revisionsOf(t, db, pid) {
		if r.ChangeID == change {
			n++
		}
	}
	return n
}
