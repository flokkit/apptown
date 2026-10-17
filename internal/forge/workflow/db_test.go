package workflow

import (
	"os"
	"sync/atomic"
	"testing"
	"time"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/forge/dbschema"
	"flokkit.io/forge/internal/gitexec/gittest"
	"flokkit.io/forge/internal/ids"
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
	if _, err := db.Exec(q, append([]any{id}, args...)...); err != nil {
		t.Fatal(err)
	}
	return id
}
func completionExec(t *testing.T, db *database.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}
func completionCount(t *testing.T, db *database.DB, want int, q string, args ...any) {
	t.Helper()
	var got int
	if err := db.QueryRow(q, args...).Scan(&got); err != nil || got != want {
		t.Fatalf("count %d want %d: %v", got, want, err)
	}
}

// fixtureNumbers gives fixture rows a number within their project that no
// other fixture row has: numbers are unique per project, and the counter
// is shared by every test in the package.
var fixtureNumbers atomic.Int64

func fixtureNumber() int64 { return 1000 + fixtureNumbers.Add(1) }
