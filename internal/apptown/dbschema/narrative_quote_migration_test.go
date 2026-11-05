package dbschema

import (
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/ids"
)

func TestMigration209RetiresShortfallsWithoutPersonQuotes(t *testing.T) {
	t.Parallel()
	db := releaseDB(t)
	if _, err := applyMigrations(t.Context(), db, migrations[:migrationIndex(t, 209)]); err != nil {
		t.Fatal(err)
	}
	pid := ids.New(time.Now())
	// sql-guarantee: seed the pre-quote schema; current publication cannot create unattributed shortfalls.
	if _, err := db.SQL().ExecContext(t.Context(), `INSERT INTO projects(id,slug,name,serving_label,created_at) VALUES($1,'quotes','Quotes','quotes',now())`, pid); err != nil {
		t.Fatal(err)
	}
	for i, met := range []string{"yes", "partly", "no", "unknown"} {
		if _, err := db.SQL().ExecContext(t.Context(), `INSERT INTO changes(id,project_id,number,type,title,created_at,summary,request_met,request_gap) VALUES($1,$2,$3,'feature','Request',now(),'Keep the work report',$4,'Old assessment')`, ids.New(time.Now()), pid, i+1, met); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := applyMigrations(t.Context(), db, migrations); err != nil {
		t.Fatal(err)
	}
	for i, oldMet := range []string{"yes", "partly", "no", "unknown"} {
		var met, gap, quote, summary string
		if err := db.SQL().QueryRowContext(t.Context(), `SELECT request_met,request_gap,request_gap_quote,summary FROM changes WHERE project_id=$1 AND number=$2`, pid, i+1).Scan(&met, &gap, &quote, &summary); err != nil {
			t.Fatal(err)
		}
		wantMet, wantGap := oldMet, "Old assessment"
		if oldMet == "partly" || oldMet == "no" {
			wantMet, wantGap = "unknown", ""
		}
		if met != wantMet || gap != wantGap || quote != "" || summary != "Keep the work report" {
			t.Fatalf("%s migrated to %s / %q / %q / %q", oldMet, met, gap, quote, summary)
		}
	}
}
