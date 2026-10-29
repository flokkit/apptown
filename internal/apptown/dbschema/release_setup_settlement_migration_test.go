package dbschema

import (
	"context"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/ids"
)

// Migration 188 removes only imported, never-chosen release destinations,
// keeps chosen and cleared preferences with their revisions, drops the
// explicit deployment promotion policy, and leaves admitted release requests'
// recorded promotion need alone.
func TestMigration188SettlesImportedSetupAndKeepsChoicesAndAdmittedNeeds(t *testing.T) {
	db := releaseDB(t)
	index := -1
	for i, m := range migrations {
		if m.n == 188 {
			index = i
			break
		}
	}
	if index < 0 {
		t.Fatal("expected migration 188")
	}
	if _, err := applyMigrations(t.Context(), db, migrations[:index]); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.SQL().ExecContext(context.Background(), query, args...); err != nil {
			t.Fatal(err)
		}
	}
	legacy, chosen, cleared, target, dep, release := ids.New(now), ids.New(now), ids.New(now), ids.New(now), ids.New(now), ids.New(now)
	// sql-guarantee: the rows are the 187 storage shape, which no current owner operation can write.
	exec(`INSERT INTO targets(id,name,kind,provider,created_at,updated_at) VALUES ($1,'settlement-host','portainer','portainer-deploy',$2,$2)`, target, now)
	for _, id := range []ids.ID{legacy, chosen, cleared} {
		exec(`INSERT INTO projects(id,slug,name,serving_label,created_at) VALUES ($1,$2,$2,$2,$3)`, id, "settle-"+id.String(), now)
	}
	exec(`INSERT INTO deployments(id,project_id,name,target_id,updated_at,require_promotion,policy_revision) VALUES ($1,$2,'customer',$3,$4,true,3)`, dep, chosen, target, now)
	exec(`INSERT INTO project_release_settings(project_id,default_deployment_id,revision,legacy_setup_json) VALUES ($1,NULL,1,'{"version":1,"deploymentName":"customer"}'),($2,$3,9,NULL),($4,NULL,7,NULL)`, legacy, chosen, dep, cleared)
	exec(`INSERT INTO releases(id,project_id,number,created_at,state) VALUES ($1,$2,1,$3,'releasing')`, release, chosen, now)
	exec(`INSERT INTO release_requests(release_id,target_id,base_generation,attributes_json,require_promotion) VALUES ($1,$2,0,'{"audience":"team","data":"synthetic"}',true)`, release, target)

	if _, err := applyMigrations(t.Context(), db, migrations[:index+1]); err != nil {
		t.Fatal(err)
	}

	var n int
	if err := db.SQL().QueryRowContext(context.Background(), `SELECT count(*) FROM project_release_settings WHERE project_id=$1`, legacy).Scan(&n); err != nil || n != 0 {
		t.Fatalf("imported setup kept: %d %v", n, err)
	}
	var selected ids.ID
	var revision int64
	if err := db.SQL().QueryRowContext(context.Background(), `SELECT default_deployment_id,revision FROM project_release_settings WHERE project_id=$1`, chosen).Scan(&selected, &revision); err != nil || selected != dep || revision != 9 {
		t.Fatalf("chosen preference changed: %s %d %v", selected, revision, err)
	}
	if err := db.SQL().QueryRowContext(context.Background(), `SELECT revision FROM project_release_settings WHERE project_id=$1 AND default_deployment_id IS NULL`, cleared).Scan(&revision); err != nil || revision != 7 {
		t.Fatalf("cleared preference changed: %d %v", revision, err)
	}
	var admitted bool
	if err := db.SQL().QueryRowContext(context.Background(), `SELECT require_promotion FROM release_requests WHERE release_id=$1`, release).Scan(&admitted); err != nil || !admitted {
		t.Fatalf("admitted promotion need lost: %v %v", admitted, err)
	}
	for _, column := range []struct{ table, name string }{{"deployments", "require_promotion"}, {"deployments", "policy_revision"}, {"project_release_settings", "legacy_setup_json"}} {
		if err := db.SQL().QueryRowContext(context.Background(), `SELECT count(*) FROM information_schema.columns WHERE table_schema=current_schema() AND table_name=$1 AND column_name=$2`, column.table, column.name).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%s.%s remains: %d %v", column.table, column.name, n, err)
		}
	}
}
