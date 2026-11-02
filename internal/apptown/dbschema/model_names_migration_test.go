package dbschema

import (
	"testing"

	"github.com/flokkit/apptown/internal/dbtest"
)

func TestMigration201RenamesCellsAndBothFallbackCoordinates(t *testing.T) {
	t.Parallel()
	db := releaseDB(t)
	if _, err := applyMigrations(t.Context(), db, migrations[:migrationIndex(t, 201)]); err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: seed the previous schema's names, including custom edges.
	if _, err := db.SQL().ExecContext(t.Context(), `DELETE FROM model_slots; DELETE FROM slot_fallbacks;
 INSERT INTO model_slots(slot,profile,pattern,updated_at) VALUES
 ('default','standard','vendor/strong','2026-01-01Z'),('default','high','vendor/high','2026-01-02Z'),
 ('primary-fast','luna','vendor/quick','2026-01-03Z'),('review','standard','vendor/review','2026-01-04Z');
 INSERT INTO slot_fallbacks(slot,fallback) VALUES ('default','custom'),('primary-fast','default'),('custom','primary-fast'),('review','secondary-strong');
 INSERT INTO audit_log(id,at,kind,subject,payload_json) VALUES ('00000000-0000-7000-8000-000000000001','2026-01-01Z','model-slot.set','default','{"slot":"default","fallback":"primary-fast"}')`); err != nil {
		t.Fatal(err)
	}
	snapshot := func() string {
		t.Helper()
		var got string
		if err := db.SQL().QueryRowContext(t.Context(), `SELECT jsonb_agg(jsonb_build_array(replace(replace(slot,'default','main-strong'),'primary-fast','main-fast'),profile,pattern,updated_at) ORDER BY profile,pattern)::text FROM model_slots`).Scan(&got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	before := snapshot()
	restore := dbtest.NextStatementFailure(db, "UPDATE slot_fallbacks SET fallback=")
	if _, err := Migrate(t.Context(), db); err == nil {
		t.Fatal("failed migration succeeded")
	}
	restore()
	var old int
	if err := db.SQL().QueryRowContext(t.Context(), `SELECT count(*) FROM model_slots WHERE slot IN ('default','primary-fast')`).Scan(&old); err != nil || old != 3 {
		t.Fatalf("rollback: %d %v", old, err)
	}
	if n, err := applyMigrations(t.Context(), db, migrations[:migrationIndex(t, 201)+1]); err != nil || n != 1 {
		t.Fatalf("upgrade: %d %v", n, err)
	}
	if after := snapshot(); after != before {
		t.Fatalf("cells changed beyond names: %s -> %s", before, after)
	}
	if err := db.SQL().QueryRowContext(t.Context(), `SELECT (SELECT count(*) FROM model_slots WHERE slot IN ('default','primary-fast')) + (SELECT count(*) FROM slot_fallbacks WHERE slot IN ('default','primary-fast') OR fallback IN ('default','primary-fast'))`).Scan(&old); err != nil || old != 0 {
		t.Fatalf("old names remain: %d %v", old, err)
	}
	rows, err := db.SQL().QueryContext(t.Context(), `SELECT slot,fallback FROM slot_fallbacks ORDER BY slot`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	want := map[string]string{"main-strong": "custom", "main-fast": "main-strong", "custom": "main-fast", "review": "secondary-strong"}
	for rows.Next() {
		var slot, fallback string
		if err := rows.Scan(&slot, &fallback); err != nil {
			t.Fatal(err)
		}
		if want[slot] != fallback {
			t.Errorf("edge %s -> %s", slot, fallback)
		}
		delete(want, slot)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(want) != 0 {
		t.Fatalf("missing edges: %v", want)
	}
	var historical string
	if err := db.SQL().QueryRowContext(t.Context(), `SELECT subject || ':' || (payload_json->>'slot') || ':' || (payload_json->>'fallback') FROM audit_log WHERE id='00000000-0000-7000-8000-000000000001'`).Scan(&historical); err != nil || historical != "default:default:primary-fast" {
		t.Fatalf("historical receipt changed: %s %v", historical, err)
	}
	if n, err := applyMigrations(t.Context(), db, migrations[:migrationIndex(t, 201)+1]); err != nil || n != 0 {
		t.Fatalf("repeat: %d %v", n, err)
	}
}
