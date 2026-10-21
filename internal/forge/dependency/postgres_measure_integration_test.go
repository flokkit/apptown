//go:build integration

package dependency

import (
	"strings"
	"testing"

	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/deploy/docker/dockertest"
)

// The stock Postgres measure, read before and after every release, only
// reads: on a database whose expression index calls a function that writes
// (which ANALYZE would run, as a review found), measuring writes nothing,
// and it reports the tables, rows and size there are.
func TestIntegrationThePostgresMeasureOnlyReads(t *testing.T) {
	stack, dk, own, psql := standingPostgres(t, "measure")
	kind := stockPostgres(t)
	if kind.Local.Measure == "" {
		t.Fatal("the stock Postgres declares no measure")
	}
	svc := kind.ResolveLocal(map[string]string{"database": "app", "user": "app"}, "", nil).Spec()
	if _, err := docker.EnsureStandingServices(t.Context(), dk, stack, []deploy.ServiceSpec{svc}, "production", own); err != nil {
		t.Fatal(err)
	}
	healthy(t, stack)
	password := svc.Env["POSTGRES_PASSWORD"]
	// sql-guarantee: the application's own database, inside its container, not AppTown's.
	setup := `CREATE TABLE witness_calls(n integer);
CREATE TABLE payload(n integer);
INSERT INTO payload SELECT generate_series(1,120);
CREATE FUNCTION index_key(integer) RETURNS integer LANGUAGE plpgsql IMMUTABLE AS $$BEGIN RETURN $1; END$$;
CREATE INDEX payload_key ON payload(index_key(n));
CREATE OR REPLACE FUNCTION index_key(integer) RETURNS integer LANGUAGE plpgsql VOLATILE AS $$BEGIN INSERT INTO witness_calls VALUES ($1); RETURN $1; END$$;
ANALYZE payload;
TRUNCATE witness_calls;`
	if out, err := psql("app", password, "app", setup); err != nil {
		t.Fatalf("setup: %v %s", err, out)
	}
	out, err := dockertest.CombinedOutput("exec", docker.ServiceName(stack, "postgres"), "sh", "-c", kind.Local.Measure)
	if err != nil {
		t.Fatalf("the measure: %v %s", err, out)
	}
	got := string(out)
	if !strings.Contains(got, "tables 2") || !strings.Contains(got, "rows 120") || !strings.Contains(got, "bytes ") {
		t.Fatalf("the measure printed %q", got)
	}
	// sql-guarantee: the application's own database, inside its container, not AppTown's.
	if n, err := psql("app", password, "app", "select count(*) from witness_calls"); err != nil || n != "0" {
		t.Fatalf("measuring ran the app's function: %q %v", n, err)
	}
}
