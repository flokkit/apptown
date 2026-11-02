package dbschema

import (
	"testing"

	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestMigration203RetiresOnlyPendingHostReadingJobsAndPreservesActMark(t *testing.T) {
	t.Parallel()
	db := releaseDB(t)
	if _, err := applyMigrations(t.Context(), db, migrations[:migrationIndex(t, 203)]); err != nil {
		t.Fatal(err)
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.SQL().ExecContext(t.Context(), query, args...); err != nil {
			t.Fatal(err)
		}
	}
	// sql-guarantee: seed the released schema and River's optional queue, before
	// the cutover. No current owner operation can create the removed job kinds.
	exec(`CREATE TABLE river_job (args jsonb, state text)`)
	for _, kind := range []string{"host-snapshot", "host-snapshot-pass", "inventory"} {
		for _, state := range []string{"available", "pending", "retryable", "scheduled", "running", "completed"} {
			exec(`INSERT INTO river_job(args,state) VALUES(jsonb_build_object('operation',$1::text),$2)`, kind, state)
		}
	}
	exec(`INSERT INTO deployment_readings(id,subject,kind,identity,invalidated,payload) VALUES($1,'host','host-snapshot','1',TRUE,'{"kept":true}')`, idstest.N(81))
	if _, err := Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"host-snapshot", "host-snapshot-pass", "inventory"} {
		want := 2
		if kind == "inventory" {
			want = 6
		}
		var got int
		if err := db.SQL().QueryRowContext(t.Context(), `SELECT count(*) FROM river_job WHERE args->>'operation'=$1`, kind).Scan(&got); err != nil || got != want {
			t.Fatalf("%s queue: %d, want %d; %v", kind, got, want, err)
		}
	}
	var act, complete int64
	var kept bool
	if err := db.SQL().QueryRowContext(t.Context(), `SELECT act_counter,complete_act_counter,(payload->>'kept')::boolean FROM deployment_readings WHERE subject='host'`).Scan(&act, &complete, &kept); err != nil || act <= complete || !kept {
		t.Fatalf("cutover lost evidence or act: %d/%d %v %v", act, complete, kept, err)
	}
}
