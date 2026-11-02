package dbschema

import (
	"testing"

	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// The upgrade discards project mappings and preserves every instance cell,
// including timestamps. A failed upgrade leaves both data and schema intact.
func TestMigration198KeepsOnlyProfileCells(t *testing.T) {
	t.Parallel()
	db := releaseDB(t)
	var previous []migration
	var upgrade migration
	for _, m := range migrations {
		if m.n < 198 {
			previous = append(previous, m)
		}
		if m.n == 198 {
			upgrade = m
		}
	}
	if len(upgrade.stmts) == 0 {
		t.Fatal("missing profile-only migration")
	}
	through198 := append(previous, upgrade)
	if _, err := applyMigrations(t.Context(), db, previous); err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: isolate historical model rows in the release fixture.
	if _, err := db.SQL().ExecContext(t.Context(), `DELETE FROM model_slots`); err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: legacy overrides cannot be created by the current model owner.
	if _, err := db.SQL().ExecContext(t.Context(), `INSERT INTO model_slots(slot,project_id,profile,pattern,updated_at) VALUES ('review',NULL,'standard','cloud/std','2026-01-01Z'),('review',NULL,'high','cloud/high','2026-01-02Z'),('review',$1,'','local/override','2026-01-03Z'),('default',$2,'','local/eval','2026-01-04Z')`, idstest.N(1), idstest.N(2)); err != nil {
		t.Fatal(err)
	}
	snapshot := func() string {
		t.Helper()
		var got string
		// sql-guarantee: compare all surviving cell fields, independently of owner reads.
		if err := db.SQL().QueryRowContext(t.Context(), `SELECT jsonb_agg(to_jsonb(m)-'project_id' ORDER BY slot,profile)::text FROM model_slots m WHERE profile <> ''`).Scan(&got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	before := snapshot()
	restore := dbtest.NextStatementFailure(db, "ALTER TABLE model_slots ADD PRIMARY KEY")
	if _, err := applyMigrations(t.Context(), db, through198); err == nil {
		t.Fatal("failed migration succeeded")
	}
	restore()
	var overrides int
	if err := db.SQL().QueryRowContext(t.Context(), `SELECT count(*) FROM model_slots WHERE project_id IS NOT NULL`).Scan(&overrides); err != nil || overrides != 2 {
		t.Fatalf("failed migration removed overrides: %d %v", overrides, err)
	}
	if _, err := applyMigrations(t.Context(), db, through198); err != nil {
		t.Fatal(err)
	}
	if after := snapshot(); after != before {
		t.Fatalf("instance cells changed: %s -> %s", before, after)
	}
	var cells, columns int
	if err := db.SQL().QueryRowContext(t.Context(), `SELECT count(*) FROM model_slots`).Scan(&cells); err != nil || cells != 2 {
		t.Fatalf("surviving cells: %d %v", cells, err)
	}
	if err := db.SQL().QueryRowContext(t.Context(), `SELECT count(*) FROM information_schema.columns WHERE table_name='model_slots' AND column_name='project_id'`).Scan(&columns); err != nil || columns != 0 {
		t.Fatalf("project column remains: %d %v", columns, err)
	}
}
