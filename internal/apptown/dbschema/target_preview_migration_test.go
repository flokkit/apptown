package dbschema

import (
	"github.com/flokkit/apptown/internal/ids"
	"testing"
	"time"
)

func TestMigration217DropsOnlyUnusedTargetPreviewSetting(t *testing.T) {
	t.Parallel()
	db := releaseDB(t)
	if _, err := applyMigrations(t.Context(), db, migrations[:migrationIndex(t, 217)]); err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: the removed field can only be seeded in the old storage shape.
	if _, err := db.SQL().ExecContext(t.Context(), `INSERT INTO targets(id,name,kind,capabilities_json,ingress_json,created_at,updated_at) VALUES($1,'host','docker-host','{"previews":true,"persistentVolumes":true,"ownLiveness":false}','{"domain":"apps.example","evals":true}',now(),now())`, ids.New(time.Now())); err != nil {
		t.Fatal(err)
	}
	if _, err := applyMigrations(t.Context(), db, migrations); err != nil {
		t.Fatal(err)
	}
	var preview, disks, evals bool
	if err := db.SQL().QueryRowContext(t.Context(), `SELECT capabilities_json ? 'previews',(capabilities_json->>'keepsDataDir')::boolean,(ingress_json->>'evals')::boolean FROM targets WHERE name='host'`).Scan(&preview, &disks, &evals); err != nil {
		t.Fatal(err)
	}
	if preview || !disks || !evals {
		t.Fatalf("removed or retained wrong settings: preview=%v disks=%v evals=%v", preview, disks, evals)
	}
}
