package dbschema

import (
	"testing"

	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestMigrationUnneededCallsAreNotApplicable(t *testing.T) {
	t.Parallel()
	db := releaseDB(t)
	if _, err := applyMigrations(t.Context(), db, migrations[:migrationIndex(t, 240)]); err != nil {
		t.Fatal(err)
	}
	project, run := idstest.N(900001), idstest.N(900002)
	// sql-guarantee: current operations cannot create the obsolete notNeeded outcome.
	if _, err := db.SQL().ExecContext(t.Context(), `INSERT INTO projects(id,slug,name,serving_label,created_at) VALUES($1,'calls','Calls','calls',now())`, project); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().ExecContext(t.Context(), `INSERT INTO runs(id,project_id,number,workflow_name,workflow_version,kind,status,current_stage,definition_json,created_at,updated_at) VALUES($1,$2,1,'scan',1,'working','done','scan','{}',now(),now())`, run, project); err != nil {
		t.Fatal(err)
	}
	for i, tc := range []struct{ kind, status, evidence string }{
		{"workflow/call", "pass", `{"notNeeded":true,"reason":"nothing fixable"}`},
		{"workflow/call", "pass", `{"childRunId":"child"}`},
		{"scan", "pass", `{"notNeeded":true}`},
		{"workflow/call", "fail", `{"notNeeded":true}`},
	} {
		if _, err := db.SQL().ExecContext(t.Context(), `INSERT INTO steps(id,run_id,stage,name,type,status,evidence_json) VALUES($1,$2,'scan','fix',$3,$4,$5)`, idstest.N(uint64(900010+i)), run, tc.kind, tc.status, tc.evidence); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"not_applicable", "pass", "pass", "fail"} {
		var status, evidence string
		if err := db.SQL().QueryRowContext(t.Context(), `SELECT status,evidence_json FROM steps WHERE id=$1`, idstest.N(uint64(900010+i))).Scan(&status, &evidence); err != nil {
			t.Fatal(err)
		}
		if status != want || i == 0 && evidence != `{"reason": "nothing fixable"}` {
			t.Fatalf("step %d: %s %s", i, status, evidence)
		}
	}
}
