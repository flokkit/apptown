package dbschema

import (
	"database/sql"
	"testing"
)

// Migration 165 abandons a change the backlog left proposed, since AppTown
// no longer has that state. It records no closure time: when the change
// stopped being wanted is not known, and its creation time is not it. A
// change in another state is left as it was.
//
// Kills: a migration that leaves proposed changes, closes them at another
// recorded time (or the database's clock), or touches other changes.
func TestMigration165AbandonsProposedChangesWithoutAClosureTime(t *testing.T) {
	t.Parallel()
	db := releaseDB(t)
	const project = "00000000-0001-7000-8000-000000000000"
	const proposed, working = "01a16603-dbee-7000-b8fe-5f23e3abfb97", "01a16603-dbee-7000-b8fe-5f23e3abfb98"
	// sql-guarantee: the release's records before the migration; the build
	// that wrote them is not this one, and this one cannot make a proposed
	// change.
	for _, row := range []struct{ id, state string }{{proposed, "proposed"}, {working, "in_progress"}} {
		if _, err := db.Exec(`INSERT INTO changes (id, project_id, number, type, title, state, created_at) VALUES (?, ?, (SELECT COALESCE(MAX(number),0)+1 FROM changes WHERE project_id = ?), 'feature', 'Show prices in euros', ?, '2026-10-13T00:00:00Z')`, row.id, project, project, row.state); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: dbschema owns migrations and reads no domain owner;
	// the stored state and closure time are the guarantee.
	read := func(id string) (string, sql.NullTime) {
		t.Helper()
		var state string
		var closed sql.NullTime
		if err := db.QueryRow(`SELECT state, closed_at FROM changes WHERE id = ?`, id).Scan(&state, &closed); err != nil {
			t.Fatal(err)
		}
		return state, closed
	}
	if state, closed := read(proposed); state != "abandoned" || closed.Valid {
		t.Fatalf("the proposed change after migration 165: %s, closed %v", state, closed)
	}
	if state, closed := read(working); state != "in_progress" || closed.Valid {
		t.Fatalf("an in-progress change after migration 165: %s, closed %v", state, closed)
	}
	var left int
	// sql-guarantee: no proposed change is left anywhere, which no owner read can list.
	if err := db.QueryRow(`SELECT COUNT(*) FROM changes WHERE state = 'proposed'`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("proposed changes left: %d %v", left, err)
	}
}
