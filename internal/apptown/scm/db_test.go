package scm

import (
	"context"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/dbschema"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"github.com/flokkit/apptown/internal/ids"
)

var databaseSuite = dbtest.NewSuite(dbschema.Open)

func TestMain(m *testing.M) {
	code := databaseSuite.Run(m)
	gittest.RemoveTemplates()
	os.Exit(code)
}
func testDB(t *testing.T) *database.DB { t.Helper(); return databaseSuite.New(t) }
func completionInsert(t *testing.T, db *database.DB, q string, args ...any) ids.ID {
	t.Helper()
	// The query's first column and placeholder are the new row's id.
	id := ids.New(time.Now())
	if _, err := db.SQL().ExecContext(context.Background(), q, append([]any{id}, args...)...); err != nil {
		t.Fatal(err)
	}
	return id
}
func completionExec(t *testing.T, db *database.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.SQL().ExecContext(context.Background(), q, args...); err != nil {
		t.Fatal(err)
	}
}

// ledger is every receipt of exactly one kind ("" for all), read through the
// audit owner. The read is bounded, so it refuses to answer at the bound.
func ledger(t *testing.T, db *database.DB, kind string) []audit.Entry {
	t.Helper()
	all, err := audit.Recent(context.Background(), db, audit.Filter{KindPrefix: kind, Limit: audit.MaxLimit})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) == audit.MaxLimit {
		t.Fatalf("%d or more %q receipts: a bounded read cannot count them", audit.MaxLimit, kind)
	}
	var out []audit.Entry
	for _, e := range all {
		if kind == "" || e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

// fixtureNumbers gives fixture rows a number within their project that no
// other fixture row has: numbers are unique per project, and the counter
// is shared by every test in the package.
var fixtureNumbers atomic.Int64

func fixtureNumber() int64 { return 1000 + fixtureNumbers.Add(1) }
