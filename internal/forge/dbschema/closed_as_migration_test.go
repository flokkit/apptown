package dbschema

import (
	"os"
	"path/filepath"
	"testing"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/dbtest"
)

// releaseDB is a forge as the 1.0 baseline release left it (the root
// package's upgrade fixture), at schema 147 and not yet upgraded.
func releaseDB(t *testing.T) *database.DB {
	t.Helper()
	dir := filepath.Join("..", "testdata", "upgrade", "1.0-baseline")
	schemaSQL, err := os.ReadFile(filepath.Join(dir, "release-schema.sql"))
	if err != nil {
		t.Fatal(err)
	}
	rows, err := dbtest.ReadRows(filepath.Join(dir, "rows.jsonl.gz"))
	if err != nil {
		t.Fatal(err)
	}
	db := emptyDB(t)
	// sql-guarantee: restoring a release's storage is what no owner operation can do.
	if err := dbtest.Restore(t.Context(), db, string(schemaSQL), rows); err != nil {
		t.Fatal(err)
	}
	return db
}

// Migration 148 marks the one change that was closed by hand, before the
// engine's nothing-to-change close was live, for exactly that reason
// (one change on an early installation), by its id and only while it
// is abandoned. Another abandoned change, with the same reason's words,
// stays unmarked: the label comes from the record, never from the text.
//
// Kills: a migration that marks by reason text, marks every abandoned
// change, or marks the change whatever its state.
func TestMigration148MarksOnlyTheChangeClosedByHandForNothingToChange(t *testing.T) {
	t.Parallel()
	db := releaseDB(t)
	const project = "00000000-0001-7000-8000-000000000000"
	// sql-guarantee: the release's records before the migration; the 1.0
	// build that wrote them is not this one, so no owner operation can.
	for _, row := range []struct{ id, state string }{
		{"01a0f60c-aaae-7000-9de8-0b5d465313ba", "abandoned"},
		{"01a0f60c-aaae-7000-9de8-0b5d465313bb", "abandoned"},
	} {
		if _, err := db.Exec(`INSERT INTO changes (id, project_id, number, type, title, state, created_at, closed_at) VALUES (?, ?, (SELECT COALESCE(MAX(number),0)+1 FROM changes WHERE project_id = ?), 'change', 'Update dependencies', ?, '2026-10-13T00:00:00Z', '2026-10-13T00:00:00Z')`, row.id, project, project, row.state); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	closedAs := map[string]string{}
	rows, err := db.Query(`SELECT id::text, closed_as FROM changes`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, as string
		if err := rows.Scan(&id, &as); err != nil {
			t.Fatal(err)
		}
		closedAs[id] = as
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for id, as := range closedAs {
		want := ""
		if id == "01a0f60c-aaae-7000-9de8-0b5d465313ba" {
			want = "nothing-to-change"
		}
		if as != want {
			t.Errorf("change %s closed_as %q after migration 148, want %q", id, as, want)
		}
	}
	if len(closedAs) < 3 {
		t.Fatalf("the release's changes and the two added were not all read: %d", len(closedAs))
	}
}

// A forge upgraded from a release has the shape a fresh forge is created
// with: the same columns in the same order, with the same types, defaults
// and checks. Queries read whole rows (SELECT *) by position, so a column a
// migration adds where schema.sql does not put it reads as another.
//
// Kills: a migration whose column, default or check differs from the
// owner's schema fragment, or lands in a different position.
func TestAnUpgradedForgeHasTheFreshSchemasShape(t *testing.T) {
	t.Parallel()
	upgraded := releaseDB(t)
	if _, err := Migrate(t.Context(), upgraded); err != nil {
		t.Fatal(err)
	}
	fresh := emptyDB(t)
	if _, err := Migrate(t.Context(), fresh); err != nil {
		t.Fatal(err)
	}
	shape := func(db *database.DB) map[string]bool {
		t.Helper()
		out := map[string]bool{}
		rows, err := db.Query(`SELECT 'column ' || table_name || '.' || column_name || ' #' || ordinal_position || ' ' || data_type || ' null=' || is_nullable || ' default=' || COALESCE(column_default, '')
FROM information_schema.columns WHERE table_schema = 'public'
UNION ALL
SELECT 'check ' || rel.relname || '.' || con.conname || ' ' || pg_get_constraintdef(con.oid)
FROM pg_constraint con JOIN pg_class rel ON rel.oid = con.conrelid JOIN pg_namespace ns ON ns.oid = rel.relnamespace
WHERE ns.nspname = 'public' AND con.contype = 'c'`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				t.Fatal(err)
			}
			out[line] = true
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}
	got, want := shape(upgraded), shape(fresh)
	for line := range want {
		if !got[line] {
			t.Errorf("the upgraded forge lacks %s", line)
		}
	}
	for line := range got {
		if !want[line] {
			t.Errorf("the upgraded forge has %s, which a fresh one does not", line)
		}
	}
}
