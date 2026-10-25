package dbschema

import (
	"github.com/flokkit/apptown/internal/ids/idstest"
	"testing"
)

func TestMigration177LeavesConflictingLegacyTargetUnresolved(t *testing.T) {
	db := releaseDB(t)
	index := -1
	for i, m := range migrations {
		if m.n == 177 {
			index = i
			break
		}
	}
	if index < 0 {
		t.Fatal("expected migration 177")
	}
	if _, err := applyMigrations(t.Context(), db, migrations[:index]); err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: historical configuration is deliberately inconsistent; import must preserve evidence without a usable target.
	if _, err := db.Exec(`INSERT INTO integrations(id,name,capability,provider,config_json,created_at,updated_at) VALUES ($1,'portainer-deploy','deploy','portainer','{"releaseDeployment":"customer"}','2026-10-26T00:00:00Z','2026-10-26T00:00:00Z'),($2,'other-connection','deploy','portainer','{}','2026-10-26T00:00:00Z','2026-10-26T00:00:00Z')`, idstest.N(800001), idstest.N(800002)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO targets(id,name,kind,provider,connection_id,address_json,created_at,updated_at) VALUES ($1,'intranet','portainer','portainer-deploy',$2,'{"tier":"different"}','2026-10-26T00:00:00Z','2026-10-26T00:00:00Z')`, idstest.N(800003), idstest.N(800002)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO projects(id,slug,name,purpose,serving_label,created_at) VALUES ($1,'c2-review','C2 review','ordinary','c2-review','2026-10-26T00:00:00Z')`, idstest.N(800004)); err != nil {
		t.Fatal(err)
	}
	if _, err := applyMigrations(t.Context(), db, migrations[:index+1]); err != nil {
		t.Fatal(err)
	}
	var usable bool
	if err := db.QueryRow(`SELECT legacy_setup_json->>'targetId' IS NOT NULL FROM project_release_settings WHERE project_id=$1`, idstest.N(800004)).Scan(&usable); err != nil {
		t.Fatal(err)
	}
	if usable {
		t.Fatal("conflicting target imported as a usable legacy selection")
	}
}
