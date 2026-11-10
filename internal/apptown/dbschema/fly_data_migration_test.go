package dbschema

import (
	"github.com/flokkit/apptown/internal/ids"
	"testing"
	"time"
)

func TestFlyDataUpgradeChangesOnlyTheStageOneTargetConfiguration(t *testing.T) {
	db := releaseDB(t)
	if _, err := applyMigrations(t.Context(), db, migrations[:migrationIndex(t, 222)]); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"fly-machines", "docker-host", "do-app"} {
		// sql-guarantee: an upgrade must start with the persisted target settings
		// from the previous schema, including its stale-editor revision.
		if _, err := db.SQL().ExecContext(t.Context(), `INSERT INTO targets(id,name,kind,needs_mode,capabilities_json,ingress_json,settings_revision,created_at,updated_at) VALUES($1,$2,$2,'managed','{"persistentVolumes":false,"needs":[],"ownLiveness":true}','{"domain":"apps.example"}',4,now(),now())`, ids.New(time.Now()), kind); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := applyMigrations(t.Context(), db, migrations); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"fly-machines", "docker-host", "do-app"} {
		var mode, needs, domain string
		var volumes, liveness bool
		var revision int
		if err := db.SQL().QueryRowContext(t.Context(), `SELECT needs_mode,(capabilities_json->>'persistentVolumes')::boolean,capabilities_json->'needs',(capabilities_json->>'ownLiveness')::boolean,ingress_json->>'domain',settings_revision FROM targets WHERE name=$1`, kind).Scan(&mode, &volumes, &needs, &liveness, &domain, &revision); err != nil {
			t.Fatal(err)
		}
		if !liveness || domain != "apps.example" {
			t.Fatal("unrelated settings changed")
		}
		if kind == "fly-machines" {
			if mode != "emulators" || !volumes || needs != `["postgres"]` || revision != 5 {
				t.Fatalf("Fly remains in stage-one configuration: %s %t %s %d", mode, volumes, needs, revision)
			}
		} else if mode != "managed" || volumes || needs != "[]" || revision != 4 {
			t.Fatalf("%s settings changed", kind)
		}
	}
}
