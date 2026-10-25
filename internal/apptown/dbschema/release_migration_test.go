package dbschema

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
)

// releaseDB is an AppTown as the 1.0.0-rc.7 release left it (the root
// package's upgrade fixture), at schema 167 and not yet upgraded.
func releaseDB(t *testing.T) *database.DB {
	t.Helper()
	dir := filepath.Join("..", "testdata", "upgrade", "1.0.0-rc.7")
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

// An AppTown upgraded from a release has the shape a fresh AppTown is created
// with: the same columns in the same order, with the same types, defaults
// and checks. Queries read whole rows (SELECT *) by position, so a column a
// migration adds where schema.sql does not put it reads as another.
//
// Kills: a migration whose column, default or check differs from the
// owner's schema fragment, or lands in a different position.
func TestAnUpgradedAppTownHasTheFreshSchemasShape(t *testing.T) {
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
		// A column's place is its rank among the table's columns: a dropped
		// column leaves a gap in ordinal_position that SELECT * never sees.
		rows, err := db.Query(`SELECT 'column ' || table_name || '.' || column_name || ' #' || row_number() OVER (PARTITION BY table_name ORDER BY ordinal_position) || ' ' || data_type || ' null=' || is_nullable || ' default=' || COALESCE(column_default, '')
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
			t.Errorf("the upgraded AppTown lacks %s", line)
		}
	}
	for line := range got {
		if !want[line] {
			t.Errorf("the upgraded AppTown has %s, which a fresh one does not", line)
		}
	}
}

// Both supported RC8 entry states work: a partially upgraded installation
// applies only its remaining migration, and reopening the latest is a no-op.
func TestSupportedIntermediateSchemaAndLatestReplay(t *testing.T) {
	t.Parallel()
	db := releaseDB(t)
	if _, err := applyMigrations(t.Context(), db, migrations[:1]); err != nil {
		t.Fatal(err)
	}
	if v, err := schemaVersion(db); err != nil || v != 168 {
		t.Fatalf("intermediate schema %d: %v", v, err)
	}
	if _, err := Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	if v, err := schemaVersion(db); err != nil || v != Latest() {
		t.Fatalf("upgraded schema %d: %v", v, err)
	}
	if _, err := Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
}
