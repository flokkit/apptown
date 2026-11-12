package dbschema

import (
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/ids"
)

// An existing pack with no what-if history offers its newest publication after
// migration, even when its former pointer was unset or stale. History is kept.
func TestMigration239OffersNewestWithoutAssessmentHistory(t *testing.T) {
	db := releaseDB(t)
	if _, err := applyMigrations(t.Context(), db, migrations[:migrationIndex(t, 239)]); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	pid := ids.New(now)
	// Historical pointer state cannot be produced by today's publication owner.
	if _, err := db.SQL().ExecContext(t.Context(), `INSERT INTO packs(id,name,latest_version,recommended_version,updated_at) VALUES($1,'existing',2,0,$2)`, pid, now); err != nil {
		t.Fatal(err)
	}
	for v := 1; v <= 2; v++ {
		if _, err := db.SQL().ExecContext(t.Context(), `INSERT INTO pack_versions(id,pack_id,version,spec_json,created_at,source,signer) VALUES($1,$2,$3,'{}',$4,'here','')`, ids.New(now), pid, v, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := applyMigrations(t.Context(), db, migrations); err != nil {
		t.Fatal(err)
	}
	pack, err := wow.New(db, wow.Dependencies{}).Pack(t.Context(), "existing")
	if err != nil || pack.Version != 2 {
		t.Fatalf("newest publication: %+v %v", pack, err)
	}
	var count int
	if err := db.SQL().QueryRowContext(t.Context(), `SELECT count(*) FROM information_schema.columns WHERE table_name='packs' AND column_name='recommended_version'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("pointer remains: %d %v", count, err)
	}
}
