package dbschema

import (
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/ids"
)

// An upgrade preserves true and false, removes the old key, and invalidates
// editors holding the old API shape. No runtime compatibility reader is needed.
func TestDataDirectoryRenamePreservesTargetSettings(t *testing.T) {
	db := releaseDB(t)
	if _, err := applyMigrations(t.Context(), db, migrations[:migrationIndex(t, 223)]); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"true", "false"} {
		// sql-guarantee: seed the released representation before the migration.
		if _, err := db.SQL().ExecContext(t.Context(), `INSERT INTO targets(id,name,kind,capabilities_json,settings_revision,created_at,updated_at) VALUES($1,$2,'docker-host',jsonb_build_object('persistentVolumes',$3::boolean,'ownLiveness',true),9,now(),now())`, ids.New(time.Now()), value, value); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := applyMigrations(t.Context(), db, migrations); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"true", "false"} {
		var old, live bool
		var got string
		var revision int
		if err := db.SQL().QueryRowContext(t.Context(), `SELECT capabilities_json ? 'persistentVolumes',capabilities_json->>'keepsDataDir',(capabilities_json->>'ownLiveness')::boolean,settings_revision FROM targets WHERE name=$1`, value).Scan(&old, &got, &live, &revision); err != nil {
			t.Fatal(err)
		}
		if old || got != value || !live || revision != 10 {
			t.Fatalf("%s: old=%t data=%s liveness=%t revision=%d", value, old, got, live, revision)
		}
	}
}
