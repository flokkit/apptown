package dbschema

import (
	"testing"

	"github.com/flokkit/apptown/internal/dbtest"
)

// The upgrade changes only old stock fallback values and moves explicit fast
// cells intact. Repeated startup and a failed migration do not change data.
func TestMigration199MovesFastCellsAndKeepsEditedFallbacks(t *testing.T) {
	for _, edited := range []string{"review", "fast"} {
		t.Run(edited, func(t *testing.T) {
			t.Parallel()
			db := releaseDB(t)
			if _, err := applyMigrations(t.Context(), db, migrations[:migrationIndex(t, 199)]); err != nil {
				t.Fatal(err)
			}
			// sql-guarantee: reproduce stock rows and an operator edit, and isolate
			// historical cells with fixed timestamps for an independent storage check.
			if _, err := db.SQL().ExecContext(t.Context(), `DELETE FROM model_slots; DELETE FROM slot_fallbacks;
INSERT INTO slot_fallbacks(slot,fallback) VALUES ('review','default'),('fast','default'),('security','review'),('scout','fast');
INSERT INTO model_slots(slot,profile,pattern,updated_at) VALUES
('default','standard','vendor/strong','2026-01-01Z'),('fast','standard','vendor/quick','2026-01-02Z'),
('fast','high','vendor/high-quick','2026-01-03Z'),('review','high','other/review','2026-01-04Z'),
('default','luna','local/luna','2026-01-05Z')`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.SQL().ExecContext(t.Context(), `UPDATE slot_fallbacks SET fallback='operator-choice' WHERE slot=$1`, edited); err != nil {
				t.Fatal(err)
			}
			snapshot := func() string {
				t.Helper()
				var got string
				// sql-guarantee: compare all cell fields, including unchanged timestamps,
				// with only the intended slot name transformed in the old snapshot.
				if err := db.SQL().QueryRowContext(t.Context(), `SELECT jsonb_agg(jsonb_build_object('slot',CASE WHEN slot='fast' THEN 'primary-fast' ELSE slot END,'profile',profile,'pattern',pattern,'updated_at',updated_at) ORDER BY profile,pattern)::text FROM model_slots`).Scan(&got); err != nil {
					t.Fatal(err)
				}
				return got
			}
			before := snapshot()
			restore := dbtest.NextStatementFailure(db, "UPDATE model_slots SET slot='primary-fast'")
			if _, err := Migrate(t.Context(), db); err == nil {
				t.Fatal("failed migration succeeded")
			}
			restore()
			var fallback string
			stock := "fast"
			want := "primary-fast"
			if edited == "fast" {
				stock, want = "review", "secondary-strong"
			}
			if err := db.SQL().QueryRowContext(t.Context(), `SELECT fallback FROM slot_fallbacks WHERE slot=$1`, stock).Scan(&fallback); err != nil || fallback != "default" {
				t.Fatalf("failed migration changed stock fallback: %q %v", fallback, err)
			}
			if n, err := Migrate(t.Context(), db); err != nil || n != 1 {
				t.Fatalf("upgrade: %d %v", n, err)
			}
			if after := snapshot(); before != after {
				t.Fatalf("cells changed beyond the move: %s -> %s", before, after)
			}
			if err := db.SQL().QueryRowContext(t.Context(), `SELECT fallback FROM slot_fallbacks WHERE slot=$1`, stock).Scan(&fallback); err != nil || fallback != want {
				t.Fatalf("stock fallback: %q want %q (%v)", fallback, want, err)
			}
			if err := db.SQL().QueryRowContext(t.Context(), `SELECT fallback FROM slot_fallbacks WHERE slot=$1`, edited).Scan(&fallback); err != nil || fallback != "operator-choice" {
				t.Fatalf("edited fallback: %q %v", fallback, err)
			}
			var count int
			if err := db.SQL().QueryRowContext(t.Context(), `SELECT count(*) FROM model_slots WHERE slot='fast' OR (slot='primary-fast' AND profile='luna')`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("fast remains or unset cell materialized: %d %v", count, err)
			}
			if n, err := Migrate(t.Context(), db); err != nil || n != 0 {
				t.Fatalf("repeat upgrade: %d %v", n, err)
			}
		})
	}
}
