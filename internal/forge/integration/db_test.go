package integration

import (
	"context"
	"os"
	"testing"
	"time"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/dbschema"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/ids"
)

var databaseSuite = dbtest.NewSuite(dbschema.Open)

func TestMain(m *testing.M)            { os.Exit(databaseSuite.Run(m)) }
func testDB(t *testing.T) *database.DB { t.Helper(); return databaseSuite.New(t) }

func createTestProject(t *testing.T, db *database.DB, slug, name string) ids.ID {
	t.Helper()
	id, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: slug, Name: name})
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// integrationReceipts is every receipt of one kind about one subject, read through the
// audit owner; a bounded read refuses to count at its bound.
func integrationReceipts(t *testing.T, db *database.DB, kind, subject string) []audit.Entry {
	t.Helper()
	all, err := audit.Recent(context.Background(), db, audit.Filter{KindPrefix: kind, Subject: subject, Limit: audit.MaxLimit})
	if err != nil || len(all) == audit.MaxLimit {
		t.Fatalf("receipts %d: %v", len(all), err)
	}
	var out []audit.Entry
	for _, e := range all {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}
