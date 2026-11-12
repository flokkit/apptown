package dbschema

import (
	"fmt"
	"testing"

	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestMigrationRetainsExplicitSourceScannerExpectations(t *testing.T) {
	t.Parallel()
	db := releaseDB(t)
	for _, m := range migrations {
		if m.n >= 230 {
			break
		}
		if err := applyMigration(t.Context(), db, m); err != nil {
			t.Fatal(err)
		}
	}
	// sql-guarantee: only a pre-migration writer can omit expectedScanners.
	project := idstest.N(900001)
	if _, err := db.SQL().ExecContext(t.Context(), `INSERT INTO projects(id,slug,name,serving_label,created_at) VALUES($1,'scan-migration','Scan migration','scan-migration',now())`, project); err != nil {
		t.Fatal(err)
	}
	for i, status := range []string{"done", "running", "abandoned", "purged"} {
		run := idstest.N(uint64(900010 + i))
		if status != "purged" {
			if _, err := db.SQL().ExecContext(t.Context(), `INSERT INTO runs(id,project_id,number,workflow_name,workflow_version,kind,status,current_stage,definition_json,created_at,updated_at) VALUES($1,$2,$3,'scan',1,'working',$4,'scan','{}',now(),now())`, run, project, i+1, status); err != nil {
				t.Fatal(err)
			}
		}
		for j, scanner := range []string{"osv", "gitleaks"} {
			if _, err := db.SQL().ExecContext(t.Context(), `INSERT INTO scan_observations(id,project_id,run_id,step_id,attempt,subject,scanner,observed_at,receipt) VALUES($1,$2,$3,$1,1,'source',$4,now(),$5)`, idstest.N(uint64(900100+i*10+j)), project, run, scanner, fmt.Sprintf(`{"subject":{"on":"source"},"scanner":%q,"reason":"unchanged"}`, scanner)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{`["gitleaks", "osv"]`, `[]`, `[]`, `[]`} {
		var got, reason string
		if err := db.SQL().QueryRowContext(t.Context(), `SELECT receipt->'expectedScanners',receipt->>'reason' FROM scan_observations WHERE id=$1`, idstest.N(uint64(900100+i*10))).Scan(&got, &reason); err != nil {
			t.Fatal(err)
		}
		if got != want || reason != "unchanged" {
			t.Fatalf("run %d: expected=%s reason=%s; want %s", i, got, reason, want)
		}
	}
}
