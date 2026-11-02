package dbschema

import (
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/ids"
)

// The inventory cutover retires only converted Docker evidence and queued work.
func TestMigration202RetainsOtherProvidersAndLaterSliceReadings(t *testing.T) {
	t.Parallel()
	db := releaseDB(t)
	if _, err := applyMigrations(t.Context(), db, migrations[:migrationIndex(t, 202)]); err != nil {
		t.Fatal(err)
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.SQL().ExecContext(t.Context(), query, args...); err != nil {
			t.Fatal(err)
		}
	}
	// sql-guarantee: reproduce the previous release's evidence and queue records.
	// River owns its schema; this minimal table exercises the migration's optional
	// queue cleanup without booting a current-version job service.
	exec(`CREATE TABLE river_job (args jsonb, state text)`)
	for _, kind := range []string{"docker-host", "portainer", "digitalocean"} {
		exec(`INSERT INTO targets(id,name,kind,created_at,updated_at) VALUES($1,$2,$2,$3,$3)`, ids.New(time.Now()), kind, time.Now())
		for _, reading := range []string{"inventory", "capacity", "detail-Diagnostics", "detail-Resources"} {
			exec(`INSERT INTO deployment_readings(id,subject,kind,identity,payload) VALUES($1,$2,$3,'revision','{"kept":true}')`, ids.New(time.Now()), kind, reading)
		}
		for _, operation := range []string{"inventory", "capacity-observation"} {
			for _, state := range []string{"available", "pending", "retryable", "scheduled", "running", "completed"} {
				// Periodic jobs use key=name without a values.target; act jobs
				// use key=name/act and identify the target in values.
				exec(`INSERT INTO river_job(args,state) VALUES(jsonb_build_object('operation',$1::text,'key',$2::text),$3)`, operation, kind, state)
				exec(`INSERT INTO river_job(args,state) VALUES(jsonb_build_object('operation',$1::text,'key',$2::text,'values',jsonb_build_object('target',$3::text)),$4)`, operation, kind+"/act", kind, state)
			}
		}
	}
	if _, err := Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	var readings, jobs int
	if err := db.SQL().QueryRowContext(t.Context(), `SELECT count(*) FROM deployment_readings WHERE subject='docker-host'`).Scan(&readings); err != nil || readings != 3 {
		t.Fatalf("Docker later slices lost: %d %v", readings, err)
	}
	if err := db.SQL().QueryRowContext(t.Context(), `SELECT count(*) FROM river_job WHERE args->'values'->>'target'='docker-host' OR args->>'key'='docker-host'`).Scan(&jobs); err != nil || jobs != 16 {
		t.Fatalf("Docker queue cleanup: %d %v", jobs, err)
	}
	for _, kind := range []string{"portainer", "digitalocean"} {
		if err := db.SQL().QueryRowContext(t.Context(), `SELECT count(*) FROM deployment_readings WHERE subject=$1 AND payload='{"kept":true}'`, kind).Scan(&readings); err != nil || readings != 4 {
			t.Fatalf("%s readings changed: %d %v", kind, readings, err)
		}
		if err := db.SQL().QueryRowContext(t.Context(), `SELECT count(*) FROM river_job WHERE args->'values'->>'target'=$1 OR args->>'key'=$1`, kind).Scan(&jobs); err != nil || jobs != 24 {
			t.Fatalf("%s jobs changed: %d %v", kind, jobs, err)
		}
	}
}
