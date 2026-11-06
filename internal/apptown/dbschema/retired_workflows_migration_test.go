package dbschema

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// The upgrade removes every executable/pinned reference, including copies with
// no runs, but preserves the entire historical runs table and unrelated data.
func TestMigration215RetiresDeploymentProceduresWithoutChangingRuns(t *testing.T) {
	db := releaseDB(t)
	index := migrationIndex(t, 215)
	if _, err := applyMigrations(t.Context(), db, migrations[:index]); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("testdata/ship.yaml")
	if err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: only raw writes can reproduce procedures rejected by today's
	// publisher, old pack publications, and their recorded adoption pins.
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.SQL().ExecContext(t.Context(), q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO lib_workflows(id,name,origin,updated_at) VALUES('00000000-0000-7000-8000-000000000501','ship','stock',now()) ON CONFLICT(name) DO NOTHING`)
	exec(`INSERT INTO lib_workflow_versions(id,lib_id,version,yaml,created_at,source,signer) SELECT '00000000-0000-7000-8000-000000000502',id,latest_version,$1,now(),'flokkit','old stock' FROM lib_workflows WHERE name='ship' ON CONFLICT(lib_id,version) DO UPDATE SET yaml=excluded.yaml`, string(raw))
	for i, tc := range []struct{ name, body string }{
		{"stock-deploy", `spec: {stages: [{onEnter: [{uses: "deploy"}]}]}`},
		{"stock-caller", `spec: {stages: [{steps: [{uses: workflow/call, with: {workflow: ship}}]}]}`},
		{"valid-script", "spec:\n  stages:\n    - steps:\n        - uses: script\n          with:\n            run: |\n              uses: deploy\n"},
	} {
		exec(`INSERT INTO lib_workflows(id,name,origin,updated_at) VALUES(($1)::uuid,$2,'stock',now())`, migrationID215(510+i*2), tc.name)
		exec(`INSERT INTO lib_workflow_versions(id,lib_id,version,yaml,created_at,source,signer) VALUES(($1)::uuid,($2)::uuid,1,$3,now(),'here','')`, migrationID215(511+i*2), migrationID215(510+i*2), tc.body)
	}
	if err := db.InTx(t.Context(), func(tx *database.Tx) error {
		_, err := project.CreateAt(t.Context(), tx, time.Now(), project.Spec{ID: idstest.Parse(t, "00000000-0000-7000-8000-000000000520"), Slug: "retirement-copy", Name: "Copy without runs", Purpose: project.EvalCopy})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	spec := `{"workflows":["valid-script","ship","stock-deploy","stock-caller"],"schedules":["ship","valid-script"],"guidance":["keep-me"]}`
	exec(`INSERT INTO packs(id,name,spec_json,updated_at) VALUES('00000000-0000-7000-8000-000000000521','retirement-pack',$1,now())`, spec)
	exec(`INSERT INTO pack_versions(id,pack_id,version,spec_json,declaration_json,pins_json,created_at,source,signer) VALUES('00000000-0000-7000-8000-000000000522','00000000-0000-7000-8000-000000000521',1,$1,$1,'{"workflows":{"ship":1,"stock-deploy":1,"valid-script":1},"blocks":{"keep-me":3}}',now(),'here','')`, spec)
	r := adoption.EmptyReceipt()
	r.Packs = []string{"retirement-pack"}
	r.Guidance = []adoption.ReceiptBlock{{Position: 2, Name: "keep-me", BlockID: idstest.N(7), Version: 3, Disabled: true, Tier: "platform"}}
	r.Policies = []adoption.ReceiptPolicy{{Name: "retained", Rule: "requireCapabilityBound", ConfigHash: "retained", Origin: "pack/retirement-pack"}}
	r.Extensions = []string{"lsp"}
	r.Workflows = []adoption.ReceiptWorkflow{{Name: "ship", Version: 1, Origin: "stock", YAMLHash: "old"}, {Name: "valid-script", Version: 1, YAMLHash: "kept", System: true}}
	receipt, _ := json.Marshal(r)
	exec(`INSERT INTO adoptions(project_id,revision) VALUES('00000000-0000-7000-8000-000000000520',1)`)
	exec(`INSERT INTO project_adoptions(id,project_id,revision,kind,digest,created_at,receipt_json) VALUES('00000000-0000-7000-8000-000000000523','00000000-0000-7000-8000-000000000520',1,'edit',$1,now(),$2)`, adoption.Digest(r), string(receipt))
	candidate, _ := json.Marshal(adoption.Candidate{Receipt: r, Digest: adoption.Digest(r), Kept: []string{"workflow ship"}})
	exec(`INSERT INTO changes(id,project_id,number,type,title,created_at,target_candidate_json) VALUES('00000000-0000-7000-8000-000000000524','00000000-0000-7000-8000-000000000520',1,'system','Pending adoption',now(),$1)`, string(candidate))
	exec(`INSERT INTO system_workflows(stock_name,name,updated_at) VALUES('land','ship',now()),('adopt-integration','ship',now()) ON CONFLICT(stock_name) DO UPDATE SET name='ship'`)
	exec(`INSERT INTO workflow_schedules(project_id,workflow_name,last_fired_at) VALUES('00000000-0000-7000-8000-000000000520','ship',now())`)
	var before string
	if err := db.SQL().QueryRowContext(t.Context(), `SELECT COALESCE(jsonb_agg(to_jsonb(r) ORDER BY id),'[]')::text FROM runs r`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if before == "[]" {
		t.Fatal("historical-run fixture is empty")
	}
	dbtest.FailNextStatement(t, db, "DELETE FROM lib_workflows")
	if _, err := Migrate(t.Context(), db); err == nil {
		t.Fatal("retirement failure hidden")
	}
	var count int
	if err := db.SQL().QueryRowContext(t.Context(), `SELECT count(*) FROM lib_workflows WHERE name='ship'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("failed migration lost ship: %d %v", count, err)
	}
	if v, err := Version(t.Context(), db); err != nil || v != 214 {
		t.Fatalf("partial migration version %d: %v", v, err)
	}
	if _, err := Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`SELECT count(*) FROM lib_workflows WHERE name IN ('ship','stock-deploy','stock-caller')`,
		`SELECT count(*) FROM system_workflows WHERE name='ship'`,
		`SELECT count(*) FROM workflow_schedules WHERE workflow_name='ship'`,
		`SELECT count(*) FROM project_adoptions WHERE receipt_json->'workflows' @> '[{"name":"ship"}]'`,
		`SELECT count(*) FROM packs WHERE spec_json->'workflows' ? 'ship'`,
		`SELECT count(*) FROM pack_versions WHERE spec_json->'workflows' ? 'ship' OR declaration_json->'workflows' ? 'ship' OR pins_json->'workflows' ? 'ship'`,
	} {
		if err := db.SQL().QueryRowContext(t.Context(), q).Scan(&count); err != nil || count != 0 {
			t.Errorf("retired reference remains (%s): %d %v", q, count, err)
		}
	}
	if got := wow.NewWorkflows(db).SystemFor(t.Context(), "land"); got != "land" {
		t.Fatalf("landing replacement %q", got)
	}
	if got := wow.NewWorkflows(db).SystemFor(t.Context(), "adopt-integration"); got != "adopt-integration" {
		t.Fatalf("non-landing default %q", got)
	}
	var packKept bool
	if err := db.SQL().QueryRowContext(t.Context(), `SELECT spec_json='{"workflows":["valid-script"],"schedules":["valid-script"],"guidance":["keep-me"]}'::jsonb AND declaration_json=spec_json AND pins_json='{"workflows":{"valid-script":1},"blocks":{"keep-me":3}}'::jsonb FROM pack_versions WHERE id='00000000-0000-7000-8000-000000000522'`).Scan(&packKept); err != nil || !packKept {
		t.Fatalf("unrelated pack contents changed: %v", err)
	}
	var after string
	if err := db.SQL().QueryRowContext(t.Context(), `SELECT COALESCE(jsonb_agg(to_jsonb(r) ORDER BY id),'[]')::text FROM runs r`).Scan(&after); err != nil || after != before {
		t.Fatalf("historical runs changed: %v", err)
	}
	if err := db.SQL().QueryRowContext(t.Context(), `SELECT count(*) FROM lib_workflows WHERE name='valid-script'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("script text mistaken for a step: %d %v", count, err)
	}
	var stored, digest string
	if err := db.SQL().QueryRowContext(t.Context(), `SELECT receipt_json,digest FROM project_adoptions WHERE id='00000000-0000-7000-8000-000000000523'`).Scan(&stored, &digest); err != nil {
		t.Fatal(err)
	}
	var got adoption.Receipt
	if err := json.Unmarshal([]byte(stored), &got); err != nil || len(got.Workflows) != 1 || got.Workflows[0].Name != "valid-script" || adoption.Digest(got) != digest {
		t.Fatalf("receipt/digest: %s %s %v", stored, digest, err)
	}
	if err := db.SQL().QueryRowContext(t.Context(), `SELECT target_candidate_json FROM changes WHERE id='00000000-0000-7000-8000-000000000524'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	var c adoption.Candidate
	if err := json.Unmarshal([]byte(stored), &c); err != nil || adoption.Digest(c.Receipt) != c.Digest || c.Digest != digest || len(c.Kept) != 0 {
		t.Fatalf("candidate/digest: %s %v", stored, err)
	}
	if n, err := Migrate(t.Context(), db); err != nil || n != 0 {
		t.Fatalf("replay: %d %v", n, err)
	}
}

func migrationID215(n int) string { return fmt.Sprintf("00000000-0000-7000-8000-%012d", n) }
