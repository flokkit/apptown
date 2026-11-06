package dbschema

import (
	"encoding/json"
	"github.com/flokkit/apptown/internal/apptown/adoption"
	"os"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/schema"
)

// sql-guarantee: the current publisher cannot produce removed deploy steps.
func migrateAuthoredWorkflow215(t *testing.T, repaired bool) {
	t.Helper()
	db := releaseDB(t)
	if _, err := applyMigrations(t.Context(), db, migrations[:migrationIndex(t, 215)]); err != nil {
		t.Fatal(err)
	}
	old, err := os.ReadFile("testdata/ship.yaml")
	if err != nil {
		t.Fatal(err)
	}
	const lib = "00000000-0000-7000-8000-000000000991"
	if _, err := db.SQL().ExecContext(t.Context(), `INSERT INTO lib_workflows(id,name,latest_version,updated_at) VALUES($1,'local-landing',1,now())`, lib); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().ExecContext(t.Context(), `INSERT INTO lib_workflow_versions(id,lib_id,version,yaml,created_at,source,signer) VALUES('00000000-0000-7000-8000-000000000992',$1,1,$2,now(),'here','')`, lib, strings.Replace(string(old), "name: ship", "name: local-landing", 1)); err != nil {
		t.Fatal(err)
	}
	if !repaired {
		// sql-guarantee: transitive callers must be named before any stored data changes.
		for i, callee := range []string{"local-landing", "custom-caller"} {
			name := []string{"custom-caller", "outer-caller"}[i]
			if _, err := db.SQL().ExecContext(t.Context(), `INSERT INTO lib_workflows(id,name,updated_at) VALUES($1,$2,now())`, migrationID215(994+i*2), name); err != nil {
				t.Fatal(err)
			}
			body := "spec: {stages: [{steps: [{uses: workflow/call, with: {workflow: " + callee + "}}]}]}"
			if _, err := db.SQL().ExecContext(t.Context(), `INSERT INTO lib_workflow_versions(id,lib_id,version,yaml,created_at,source,signer) VALUES($1,$2,1,$3,now(),'here','')`, migrationID215(995+i*2), migrationID215(994+i*2), body); err != nil {
				t.Fatal(err)
			}
		}
	}
	var before string
	if err := db.SQL().QueryRowContext(t.Context(), `SELECT jsonb_agg(to_jsonb(v) ORDER BY id)::text FROM lib_workflow_versions v`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if repaired {
		current, err := os.ReadFile("../../../stock/land.yaml")
		if err != nil {
			t.Fatal(err)
		}
		valid := strings.Replace(string(current), "name: land", "name: local-landing", 1)
		if _, err := schema.ParseWorkflowStored([]byte(valid)); err != nil {
			t.Fatal(err)
		}
		if _, err := db.SQL().ExecContext(t.Context(), `INSERT INTO lib_workflow_versions(id,lib_id,version,yaml,created_at,source,signer) VALUES('00000000-0000-7000-8000-000000000993',$1,2,$2,now(),'here','')`, lib, valid); err != nil {
			t.Fatal(err)
		}
		if _, err := db.SQL().ExecContext(t.Context(), `UPDATE lib_workflows SET latest_version=2 WHERE id=$1`, lib); err != nil {
			t.Fatal(err)
		}
	}
	_, err = Migrate(t.Context(), db)
	if !repaired {
		if err == nil {
			t.Fatal("upgrade succeeded after deleting institution-authored procedure; should refuse visibly and preserve it")
		}
		for _, want := range []string{"local-landing", "custom-caller", "outer-caller", "build", "delete", "upgrade again"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("missing %q in upgrade error: %v", want, err)
			}
		}
		var after string
		if err := db.SQL().QueryRowContext(t.Context(), `SELECT jsonb_agg(to_jsonb(v) ORDER BY id)::text FROM lib_workflow_versions v`).Scan(&after); err != nil {
			t.Fatal(err)
		}
		if before != after {
			t.Fatal("refused upgrade changed authored publications")
		}
		if v, err := Version(t.Context(), db); err != nil || v != 214 {
			t.Fatalf("refused upgrade version: %d %v", v, err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.SQL().QueryRowContext(t.Context(), `SELECT count(*) FROM lib_workflows WHERE id=$1 AND latest_version=2`, lib).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("upgrade deleted supported current local workflow because an older publication used deploy")
	}
}
func TestMigration215PreservesRepairedLocalWorkflow(t *testing.T) {
	t.Parallel()
	migrateAuthoredWorkflow215(t, true)
}
func TestMigration215RefusesDestructionOfAuthoredWorkflow(t *testing.T) {
	t.Parallel()
	migrateAuthoredWorkflow215(t, false)
}

// Obsolete pins and callers are removed per publication; the current land and
// a repaired caller retain their identities, selections and supported pins.
func TestMigration215PreservesSupportedPublicationsAndTheirReferences(t *testing.T) {
	t.Parallel()
	db := releaseDB(t)
	if _, err := applyMigrations(t.Context(), db, migrations[:migrationIndex(t, 215)]); err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.SQL().ExecContext(t.Context(), q, args...); err != nil {
			t.Fatal(err)
		}
	}
	read := func(path string) string {
		t.Helper()
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(body)
	}
	oldLand := read("testdata/land-deploy.yaml")
	land := read("../../../stock/land.yaml")
	ship := read("testdata/ship.yaml")
	call := func(callee string) string {
		return "apiVersion: apptown.dev/v1\nkind: Workflow\nmetadata: {name: repaired-caller}\nspec: {kind: pipeline, stages: [{name: call, steps: [{name: invoke, uses: workflow/call, with: {workflow: " + callee + "}}]}]}"
	}
	if _, err := schema.ParseWorkflowStored([]byte(call("land"))); err != nil {
		t.Fatal(err)
	}
	if _, err := schema.ParseWorkflowStored([]byte(land)); err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: released YAML with removed steps and obsolete pins cannot be published today.
	exec(`INSERT INTO lib_workflows(id,name,origin,updated_at) VALUES($1,'ship','stock',now())`, migrationID215(1100))
	exec(`INSERT INTO lib_workflow_versions(id,lib_id,version,yaml,created_at,source,signer) VALUES($1,$2,1,$3,now(),'flokkit','old stock')`, migrationID215(1101), migrationID215(1100), ship)
	for i, tc := range []struct{ name, origin, old, current string }{
		{"land", "stock", oldLand, land},
		{"repaired-caller", "", call("ship"), call("land")},
	} {
		exec(`INSERT INTO lib_workflows(id,name,origin,latest_version,updated_at) VALUES($1,$2,$3,2,now())`, migrationID215(1110+i*3), tc.name, tc.origin)
		for v, body := range []string{tc.old, tc.current} {
			exec(`INSERT INTO lib_workflow_versions(id,lib_id,version,yaml,created_at,source,signer) VALUES($1,$2,$3,$4,now(),'here','')`, migrationID215(1111+i*3+v), migrationID215(1110+i*3), v+1, body)
		}
	}
	exec(`INSERT INTO packs(id,name,spec_json,latest_version,updated_at) VALUES($1,'versioned-pack','{"workflows":["land","repaired-caller"],"schedules":["land"]}',2,now())`, migrationID215(1120))
	for v := 1; v <= 2; v++ {
		pins, _ := json.Marshal(map[string]any{"workflows": map[string]int{"land": v, "repaired-caller": v}})
		exec(`INSERT INTO pack_versions(id,pack_id,version,spec_json,declaration_json,pins_json,created_at,source,signer) SELECT $1,id,$2,spec_json,spec_json,$3,now(),'here','' FROM packs WHERE name='versioned-pack'`, migrationID215(1120+v), v, string(pins))
		receipt := adoption.EmptyReceipt()
		receipt.Workflows = []adoption.ReceiptWorkflow{{Name: "land", Version: v}, {Name: "repaired-caller", Version: v}}
		raw, _ := json.Marshal(receipt)
		exec(`INSERT INTO project_adoptions(id,project_id,revision,kind,digest,created_at,receipt_json) SELECT $1,id,$2,'edit',$3,now(),$4 FROM projects ORDER BY id LIMIT 1`, migrationID215(1130+v), 1000+v, adoption.Digest(receipt), string(raw))
		candidate, _ := json.Marshal(adoption.Candidate{Receipt: receipt, Digest: adoption.Digest(receipt), Kept: []string{"workflow land", "workflow repaired-caller"}})
		exec(`INSERT INTO changes(id,project_id,number,type,title,created_at,target_candidate_json) SELECT $1,id,$2,'system','Pending adoption',now(),$3 FROM projects ORDER BY id LIMIT 1`, migrationID215(1140+v), 1000+v, string(candidate))
	}
	exec(`INSERT INTO system_workflows(stock_name,name,updated_at) VALUES('land','land',now()) ON CONFLICT(stock_name) DO UPDATE SET name='land'`)
	if _, err := Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	for v := 1; v <= 2; v++ {
		var raw string
		if err := db.SQL().QueryRowContext(t.Context(), `SELECT target_candidate_json FROM changes WHERE id=$1`, migrationID215(1140+v)).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var candidate adoption.Candidate
		if err := json.Unmarshal([]byte(raw), &candidate); err != nil {
			t.Fatal(err)
		}
		if len(candidate.Receipt.Workflows) != (v-1)*2 || len(candidate.Kept) != (v-1)*2 || candidate.Digest != adoption.Digest(candidate.Receipt) {
			t.Fatalf("candidate v%d pins or digest changed incorrectly: %s", v, raw)
		}
	}
	for _, q := range []string{
		`SELECT count(*)=2 FROM lib_workflow_versions v JOIN lib_workflows l ON l.id=v.lib_id WHERE l.name IN ('land','repaired-caller') AND v.version=2 AND l.latest_version=2`,
		`SELECT count(*)=0 FROM lib_workflow_versions v JOIN lib_workflows l ON l.id=v.lib_id WHERE l.name IN ('land','repaired-caller') AND v.version=1`,
		`SELECT spec_json->'workflows'='["land","repaired-caller"]'::jsonb FROM packs WHERE name='versioned-pack'`,
		`SELECT spec_json->'workflows'='[]'::jsonb AND declaration_json->'workflows'='[]'::jsonb AND pins_json->'workflows'='{}'::jsonb AND spec_json->'schedules'='[]'::jsonb FROM pack_versions WHERE id='00000000-0000-7000-8000-000000001121'`,
		`SELECT spec_json->'workflows'='["land","repaired-caller"]'::jsonb AND declaration_json=spec_json AND pins_json->'workflows'='{"land":2,"repaired-caller":2}'::jsonb FROM pack_versions WHERE id='00000000-0000-7000-8000-000000001122'`,
		`SELECT name='land' FROM system_workflows WHERE stock_name='land'`,
		`SELECT receipt_json->'workflows'='[]'::jsonb FROM project_adoptions WHERE id='00000000-0000-7000-8000-000000001131'`,
		`SELECT jsonb_array_length(receipt_json->'workflows')=2 FROM project_adoptions WHERE id='00000000-0000-7000-8000-000000001132'`,
	} {
		var ok bool
		if err := db.SQL().QueryRowContext(t.Context(), q).Scan(&ok); err != nil || !ok {
			t.Errorf("publication/reference guarantee (%s): %v %v", q, ok, err)
		}
	}
}
