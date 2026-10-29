package dbschema

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/ids"
)

func TestMigration177PreservesLegacySetupWithoutRefillingClearedDefaults(t *testing.T) {
	db := releaseDB(t)
	index := -1
	for i, m := range migrations {
		if m.n == 177 {
			index = i
			break
		}
	}
	if index < 0 {
		t.Fatal("expected migration177 suffix")
	}
	if _, err := applyMigrations(t.Context(), db, migrations[:index]); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	connector, target := ids.New(now), ids.New(now)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.SQL().ExecContext(context.Background(), query, args...); err != nil {
			t.Fatal(err)
		}
	}
	// sql-guarantee: immutable upgrade evidence must be installed as released rows, before current owner columns exist.
	exec(`INSERT INTO integrations(id,name,capability,provider,config_json,created_at,updated_at) VALUES ($1,'portainer-deploy','deploy','portainer','{"releaseDeployment":"customer-test","kept":"raw"}',$2,$2) ON CONFLICT(name) DO UPDATE SET config_json=EXCLUDED.config_json`, connector, now)
	exec(`INSERT INTO targets(id,name,kind,provider,address_json,created_at,updated_at) VALUES ($1,'intranet','portainer','portainer-deploy','{"tier":"customer-test"}',$2,$2) ON CONFLICT(name) DO NOTHING`, target, now)
	var actualConnector ids.ID
	if err := db.SQL().QueryRowContext(context.Background(), `SELECT id FROM integrations WHERE name='portainer-deploy'`).Scan(&actualConnector); err != nil {
		t.Fatal(err)
	}
	exec(`UPDATE targets SET connection_id=$1 WHERE name='intranet'`, actualConnector)
	var actualTarget ids.ID
	if err := db.SQL().QueryRowContext(context.Background(), `SELECT id FROM targets WHERE name='intranet'`).Scan(&actualTarget); err != nil {
		t.Fatal(err)
	}
	never, cleared, selected := ids.New(now), ids.New(now), ids.New(now)
	dep := ids.New(now)
	for _, id := range []ids.ID{never, cleared, selected} {
		exec(`INSERT INTO projects(id,slug,name,serving_label,created_at) VALUES ($1,$2,$2,$2,$3)`, id, "policy-"+id.String(), now)
	}
	exec(`INSERT INTO deployments(id,project_id,name,target_id,updated_at) VALUES ($1,$2,'customer-test',$3,$4)`, dep, selected, actualTarget, now)
	exec(`INSERT INTO project_release_settings(project_id,default_deployment_id,revision) VALUES ($1,NULL,7),($2,$3,9)`, cleared, selected, dep)

	requests := []struct {
		id       ids.ID
		target   ids.ID
		attrs    string
		required bool
	}{
		{ids.New(now), ids.New(now), `{"audience":"public","data":"synthetic"}`, true},
		{ids.New(now), ids.New(now), `{"audience":"team","data":"production"}`, true},
		{ids.New(now), actualTarget, `{"audience":"team","data":"non-production"}`, true},
		{ids.New(now), ids.New(now), `{"audience":"team","data":"synthetic"}`, false},
	}
	for n, r := range requests {
		exec(`INSERT INTO releases(id,project_id,number,created_at,state) VALUES ($1,$2,$3,$4,'releasing')`, r.id, selected, 100+n, now)
		exec(`INSERT INTO release_requests(release_id,target_id,base_generation,attributes_json,authority_json) VALUES ($1,$2,0,$3,'{"principal":"00000000-0000-4000-8000-000000000001","kind":"user","name":"recorded","credential":"header:recorded"}')`, r.id, r.target, r.attrs)
	}
	var authorityBefore string
	if err := db.SQL().QueryRowContext(context.Background(), `SELECT authority_json::text FROM release_requests WHERE release_id=$1`, requests[0].id).Scan(&authorityBefore); err != nil {
		t.Fatal(err)
	}
	if _, err := applyMigrations(t.Context(), db, migrations[:index+1]); err != nil {
		t.Fatal(err)
	}

	for _, r := range requests {
		var required bool
		var authority string
		if err := db.SQL().QueryRowContext(context.Background(), `SELECT require_promotion,authority_json::text FROM release_requests WHERE release_id=$1`, r.id).Scan(&required, &authority); err != nil || required != r.required || authority != authorityBefore {
			t.Fatalf("admitted requirement/identity changed: %v %v %v", required, r.required, err)
		}
	}
	var candidate sql.NullString
	var revision int64
	if err := db.SQL().QueryRowContext(context.Background(), `SELECT legacy_setup_json::text,revision FROM project_release_settings WHERE project_id=$1`, cleared).Scan(&candidate, &revision); err != nil || candidate.Valid || revision != 7 {
		t.Fatalf("cleared default resurrected: %+v %d %v", candidate, revision, err)
	}
	var chosen ids.ID
	if err := db.SQL().QueryRowContext(context.Background(), `SELECT default_deployment_id,revision FROM project_release_settings WHERE project_id=$1`, selected).Scan(&chosen, &revision); err != nil || chosen != dep || revision != 9 {
		t.Fatalf("selected choice changed: %s %d %v", chosen, revision, err)
	}
	var name, raw string
	var targetID ids.ID
	if err := db.SQL().QueryRowContext(context.Background(), `SELECT legacy_setup_json->>'deploymentName',legacy_setup_json->'configuration'->>'kept',(legacy_setup_json->>'targetId')::uuid FROM project_release_settings WHERE project_id=$1`, never).Scan(&name, &raw, &targetID); err != nil || name != "customer-test" || raw != "raw" || targetID != actualTarget {
		t.Fatalf("legacy intent lost: %s %s %s %v", name, raw, targetID, err)
	}
	var required bool
	if err := db.SQL().QueryRowContext(context.Background(), `SELECT require_promotion FROM deployments WHERE id=$1`, dep).Scan(&required); err != nil || !required {
		t.Fatalf("legacy promotion gate lost: %v %v", required, err)
	}
	// A new project after migration does not inherit the old global choice.
	fresh := ids.New(now)
	exec(`INSERT INTO projects(id,slug,name,serving_label,created_at) VALUES ($1,$2,$2,$2,$3)`, fresh, "new-"+fresh.String(), now)
	var count int
	if err := db.SQL().QueryRowContext(context.Background(), `SELECT count(*) FROM project_release_settings WHERE project_id=$1`, fresh).Scan(&count); err != nil || count != 0 {
		t.Fatalf("new project inherited legacy default: %d %v", count, err)
	}
}
