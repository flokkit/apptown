package dbschema

import (
	"testing"

	"github.com/flokkit/apptown/internal/dbtest"
)

func TestMigration176DoesNotInventHistoricalReleaseAuthority(t *testing.T) {
	db := releaseDB(t)
	index := -1
	for i, m := range migrations {
		if m.n == 176 {
			index = i
			break
		}
	}
	if index < 0 {
		t.Fatal("authority migration missing")
	}
	if _, err := applyMigrations(t.Context(), db, migrations[:index]); err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: historical queued rows have no principal snapshot. Current
	// admission cannot create this old shape; never infer it from approved_by.
	if _, err := db.Exec(`INSERT INTO release_requests(release_id,target_id,base_generation,attributes_json)
 SELECT r.id,d.target_id,d.desired_generation,'{}' FROM releases r JOIN deployments d ON d.id=r.deployment_id LIMIT 1
 ON CONFLICT(release_id) DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	var requests int
	if err := db.QueryRow(`SELECT COUNT(*) FROM release_requests`).Scan(&requests); err != nil || requests == 0 {
		t.Fatalf("historical fixture has no request: %d %v", requests, err)
	}
	snapshot := func() string {
		t.Helper()
		var value string
		if err := db.QueryRow(`SELECT jsonb_agg(to_jsonb(r) ORDER BY id)::text FROM releases r`).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	before := snapshot()
	dbtest.FailNextStatement(t, db, "ALTER TABLE release_requests ADD COLUMN authority_json")
	if _, err := applyMigrations(t.Context(), db, migrations[:index+1]); err == nil {
		t.Fatal("failed migration was hidden")
	}
	if version, err := schemaVersion(db); err != nil || version != 175 {
		t.Fatalf("failed migration advanced version: %d %v", version, err)
	}
	if _, err := applyMigrations(t.Context(), db, migrations[:index+1]); err != nil {
		t.Fatal(err)
	}
	var unknown, total int
	if err := db.QueryRow(`SELECT COUNT(*) FILTER (WHERE authority_json IS NULL),COUNT(*) FROM release_requests`).Scan(&unknown, &total); err != nil || unknown != requests || total != requests {
		t.Fatalf("migration guessed or removed request authority: unknown=%d total=%d want=%d: %v", unknown, total, requests, err)
	}
	if snapshot() != before {
		t.Fatal("migration rewrote historical release attribution")
	}
}
