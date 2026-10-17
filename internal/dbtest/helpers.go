package dbtest

import (
	"testing"

	"flokkit.io/forge/internal/database"
)

// FailNextStatement makes the next statement whose text contains fragment
// fail, for the rest of the test: the fault a test injects to prove a
// failed write or read is reported, never mistaken for success.
func FailNextStatement(t testing.TB, db *database.DB, fragment string) {
	t.Helper()
	t.Cleanup(db.FailNextStatement(fragment))
}

// Count fails the test unless q, a single-count query, answers want. It is
// for storage tests whose guarantee is the stored representation; tests of
// behaviour ask the owner instead.
func Count(t testing.TB, db *database.DB, want int, q string, args ...any) {
	t.Helper()
	var got int
	if err := db.QueryRow(q, args...).Scan(&got); err != nil || got != want {
		t.Fatalf("count %d want %d: %v (%s)", got, want, err, q)
	}
}
