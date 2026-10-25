package dbschema

import (
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
	"testing"
	"time"
)

func TestMigration175CapturesExistingServingNamesAtomically(t *testing.T) {
	db := releaseDB(t)
	index := -1
	for i, m := range migrations {
		if m.n == 175 {
			index = i
			break
		}
	}
	if index < 0 {
		t.Fatal("migration175 missing")
	}
	if _, err := applyMigrations(t.Context(), db, migrations[:index]); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	kept := ids.Nil
	for _, purpose := range []string{"ordinary", "eval-copy", "deleted"} {
		id := ids.New(now)
		actual := purpose
		if purpose == "deleted" {
			actual = "ordinary"
			kept = id
		}
		// sql-guarantee: seed released pre175 rows; current CreateAt already requires the new immutable field.
		if _, err := db.Exec(`INSERT INTO projects(id,slug,name,purpose,created_at) VALUES ($1,$2,$2,$3,$4)`, id, "serving-"+purpose, actual, now); err != nil {
			t.Fatal(err)
		}
		if purpose == "deleted" {
			// sql-guarantee: retained deletion is historical input to the migration, not a current lifecycle operation.
			if _, err := db.Exec(`INSERT INTO project_lifecycle(project_id,state,requested_at) VALUES ($1,'deleted',$2)`, id, now); err != nil {
				t.Fatal(err)
			}
		}
	}
	snapshot := func() string {
		t.Helper()
		var got string
		if err := db.QueryRow(`SELECT jsonb_object_agg(id,slug)::text FROM projects`).Scan(&got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	before := snapshot()
	dbtest.FailNextStatement(t, db, "UPDATE projects SET serving_label=slug")
	if _, err := applyMigrations(t.Context(), db, migrations[:index+1]); err == nil {
		t.Fatal("backfill failure was hidden")
	}
	if version, err := schemaVersion(db); err != nil || version != 174 {
		t.Fatalf("partial version=%d: %v", version, err)
	}
	var exists bool
	if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name='projects' AND column_name='serving_label')`).Scan(&exists); err != nil || exists {
		t.Fatalf("failed migration kept field: %t %v", exists, err)
	}
	if snapshot() != before {
		t.Fatal("failed backfill changed canonical identities")
	}
	if _, err := applyMigrations(t.Context(), db, migrations[:index+1]); err != nil {
		t.Fatal(err)
	}
	var captured string
	if err := db.QueryRow(`SELECT jsonb_object_agg(id,serving_label)::text FROM projects`).Scan(&captured); err != nil || captured != before {
		t.Fatalf("serving names not captured exactly: %s want %s: %v", captured, before, err)
	}
	// sql-guarantee: simulate future canonical rename; this migration must preserve the independent original host label.
	if _, err := db.Exec(`UPDATE projects SET slug='changed-address' WHERE id=$1`, kept); err != nil {
		t.Fatal(err)
	}
	var label string
	if err := db.QueryRow(`SELECT serving_label FROM projects WHERE id=$1`, kept).Scan(&label); err != nil || label != "serving-deleted" {
		t.Fatalf("retained identity moved: %q %v", label, err)
	}
	for _, bad := range []string{"", "INVALID", "serving-ordinary"} {
		// sql-guarantee: schema constraints must refuse empty, invalid and another project's serving identity independently of Go validation.
		if _, err := db.Exec(`UPDATE projects SET serving_label=$2 WHERE id=$1`, kept, bad); err == nil {
			t.Fatalf("invalid serving label accepted: %q", bad)
		}
	}
}
