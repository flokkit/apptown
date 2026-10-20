//go:build integration

package dependency

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/deploy/docker/dockertest"
	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/poll"
	"flokkit.io/forge/stock"
)

// stockPostgres is the stock Postgres kind.
func stockPostgres(t *testing.T) *Kind {
	t.Helper()
	raw, err := stock.FS.ReadFile("dependencies/postgres.yaml")
	if err != nil {
		t.Fatal(err)
	}
	kind, err := Parse(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	return kind
}

// standingPostgres is a deployment's standing Postgres on a stack of its
// own, removed with its volume when the test ends, and a psql that reaches
// it from another container on the stack's network: never over the
// server's loopback, which trusts every connection.
func standingPostgres(t *testing.T, name string) (stack string, dk docker.Scope, own docker.Ownership, psql func(user, password, db, sql string) (string, error)) {
	t.Helper()
	stack = fmt.Sprintf("env-%s%d-main", name, os.Getpid())
	dk = docker.Scope{Client: docker.CLI{Binary: "docker"}}
	t.Cleanup(func() {
		_ = dockertest.Run("rm", "-f", docker.ServiceName(stack, "postgres"))
		_ = dockertest.Run("volume", "rm", docker.NeedVolumeName(stack, "postgres"))
		_ = dockertest.Run("network", "rm", docker.NetworkName(stack))
	})
	own = docker.Ownership{Record: func(deploy.Owned) error { return nil }, Needs: map[string]string{"postgres": "/var/lib/postgresql/data"}}
	image := strings.ReplaceAll(stockPostgres(t).Local.Image, "{version}", "16")
	psql = func(user, password, db, sql string) (string, error) {
		out, err := dockertest.CombinedOutput("run", "--rm", "--network", docker.NetworkName(stack), "-e", "PGPASSWORD="+password,
			image, "psql", "-h", "postgres", "-U", user, "-d", db, "-X", "-tA", "-v", "ON_ERROR_STOP=1", "-c", sql)
		return strings.TrimSpace(string(out)), err
	}
	return stack, dk, own, psql
}

func healthy(t *testing.T, stack string) {
	t.Helper()
	health := ""
	if !poll.Until(t.Context(), 90*time.Second, 250*time.Millisecond, func() bool {
		out, _ := dockertest.Output("inspect", "-f", "{{.State.Health.Status}}", docker.ServiceName(stack, "postgres"))
		health = strings.TrimSpace(string(out))
		return health == "healthy"
	}) {
		logs, _ := dockertest.CombinedOutput("logs", "--tail", "30", docker.ServiceName(stack, "postgres"))
		t.Fatalf("the Postgres did not mark itself ready: %s\n%s", health, logs)
	}
}

// A standing Postgres made by the postgres.yaml of before 17 October 2026
// (the image's own start, its app user the server's superuser, a password
// the app chose) holding rows, an extension, a serial and a partitioned
// table, on a deployment with real data, takes the start script on its own
// volume: the release remakes it rather than refusing, the rows are there,
// the app reaches them with AppTown's password and not its old one, and
// it is an ordinary user that owns what it made.
func TestIntegrationAnOldPostgresBecomesAnOrdinaryOwnerOnItsVolume(t *testing.T) {
	params := map[string]string{"database": "sourdough", "user": "sourdough"}
	current := stockPostgres(t).ResolveLocal(params, "", nil).Spec()
	old := deploy.ServiceSpec{Name: "postgres", Image: current.Image, Health: "pg_isready -U sourdough -d sourdough", Data: current.Data,
		Env: map[string]string{"POSTGRES_DB": "sourdough", "POSTGRES_USER": "sourdough", "POSTGRES_PASSWORD": "sourdough"}}
	stack, dk, own, psql := standingPostgres(t, "oldpg")
	if _, err := docker.EnsureStandingServices(t.Context(), dk, stack, []deploy.ServiceSpec{old}, "production", own); err != nil {
		t.Fatal(err)
	}
	var answer error
	if !poll.Until(t.Context(), 90*time.Second, 250*time.Millisecond, func() bool {
		_, answer = psql("sourdough", "sourdough", "sourdough", "select 1")
		return answer == nil
	}) {
		t.Fatalf("the old Postgres did not answer: %v", answer)
	}
	// sql-guarantee: the app's own records on a stand-in server, not AppTown's database.
	for _, sql := range []string{
		"create extension pgcrypto",
		"create table loaves (id serial primary key, name text default gen_random_uuid()::text)",
		// sql-guarantee: the app's own rows on its stand-in server, not AppTown's database.
		"insert into loaves (name) select 'loaf ' || g from generate_series(1, 50) g",
		"create table bakes (day int) partition by range (day)",
		"create table bakes_first partition of bakes for values from (0) to (10)",
	} {
		if out, err := psql("sourdough", "sourdough", "sourdough", sql); err != nil {
			t.Fatalf("%s: %v %s", sql, err, out)
		}
	}
	if su, err := psql("sourdough", "sourdough", "sourdough", "select rolsuper from pg_roles where rolname = current_user"); err != nil || su != "t" {
		t.Fatalf("the old layout's app user is the superuser: %q %v", su, err)
	}

	if _, err := docker.EnsureStandingServices(t.Context(), dk, stack, []deploy.ServiceSpec{current}, "production", own); err != nil {
		t.Fatalf("the old Postgres was not remade on its volume: %v", err)
	}
	healthy(t, stack)
	password := current.Env["POSTGRES_PASSWORD"]
	if out, err := psql("sourdough", password, "sourdough", "select count(*) from loaves"); err != nil || out != "50" {
		t.Fatalf("the rows after the remake: %q %v", out, err)
	}
	if out, err := psql("sourdough", "sourdough", "sourdough", "select 1"); err == nil || !strings.Contains(out, "password authentication failed") {
		t.Fatalf("the password the app chose still opens its database: %q %v", out, err)
	}
	if out, err := psql("sourdough", password, "sourdough", "select rolsuper::text || ' ' || (select string_agg(relname || '=' || pg_get_userbyid(relowner), ' ' order by relname) from pg_class where relname in ('loaves', 'loaves_id_seq', 'bakes', 'bakes_first')) from pg_roles where rolname = current_user"); err != nil ||
		out != "false bakes=sourdough bakes_first=sourdough loaves=sourdough loaves_id_seq=sourdough" {
		t.Fatalf("the app's user and what it owns after the remake: %q %v", out, err)
	}
	if out, err := psql("sourdough", password, "sourdough", "create role intruder"); err == nil {
		t.Fatalf("the converted app made a role: %s", out)
	}
	// It owns the extension it made, so a copy's restore can drop and make it.
	for _, sql := range []string{"drop extension pgcrypto cascade", "create extension pgcrypto"} {
		if out, err := psql("sourdough", password, "sourdough", sql); err != nil {
			t.Fatalf("%s after the remake: %v %s", sql, err, out)
		}
	}
	// The next release finds nothing moved and keeps the server as it is.
	before, _ := dockertest.Output("inspect", "-f", "{{.Id}}", docker.ServiceName(stack, "postgres"))
	if _, err := docker.EnsureStandingServices(t.Context(), dk, stack, []deploy.ServiceSpec{current}, "production", own); err != nil {
		t.Fatal(err)
	}
	if after, _ := dockertest.Output("inspect", "-f", "{{.Id}}", docker.ServiceName(stack, "postgres")); string(after) != string(before) {
		t.Fatal("a release with nothing moved remade the converted Postgres")
	}
}

// A new deployment's Postgres gives the app an ordinary user that owns its
// database: it makes tables, indexes, sequences, schemas, functions and
// trusted extensions, and is refused roles, databases, server settings,
// untrusted extensions and server programs. Another role's database made
// the same way refuses the app's role (the role ACL; apps that shared a
// password would not be separated by it), and the server's own superuser
// cannot log in over the network.
func TestIntegrationANewPostgresGivesTheAppAnOrdinaryOwner(t *testing.T) {
	current := stockPostgres(t).ResolveLocal(nil, "", nil).Spec()
	stack, dk, own, psql := standingPostgres(t, "newpg")
	if _, err := docker.EnsureStandingServices(t.Context(), dk, stack, []deploy.ServiceSpec{current}, "production", own); err != nil {
		t.Fatal(err)
	}
	healthy(t, stack)
	password := current.Env["POSTGRES_PASSWORD"]
	for _, sql := range []string{
		"create table notes (id bigint generated always as identity primary key, body text)",
		"create index on notes (body)",
		"create sequence tickets",
		"create schema reports",
		"create function twice(int) returns int language sql as 'select $1 * 2'",
		"create extension citext",
		"create extension pg_trgm",
	} {
		if out, err := psql("app", password, "app", sql); err != nil {
			t.Errorf("an owner's %q was refused: %v %s", sql, err, out)
		}
	}
	for _, sql := range []string{
		"create role intruder",
		"create database another",
		"alter system set work_mem = '1MB'",
		"create extension dblink",
		"copy (select 1) to program 'id'",
		"alter role app superuser",
	} {
		if out, err := psql("app", password, "app", sql); err == nil {
			t.Errorf("an ordinary owner was allowed %q: %s", sql, out)
		}
	}
	// sql-guarantee: the server's own superuser making another role's database, as a shared server would hold it.
	if out, err := dockertest.CombinedOutput("exec", docker.ServiceName(stack, "postgres"), "psql", "-U", "postgres", "-v", "ON_ERROR_STOP=1",
		"-c", "create role other login password 'other'", "-c", "create database other owner other", "-c", "revoke all on database other from public"); err != nil {
		t.Fatalf("another role's database: %v %s", err, out)
	}
	if out, err := psql("app", password, "other", "select 1"); err == nil || !strings.Contains(out, "permission denied for database") {
		t.Errorf("the app's role reached another role's database: %q %v", out, err)
	}
	if out, err := psql("other", "other", "app", "select 1"); err == nil || !strings.Contains(out, "permission denied for database") {
		t.Errorf("another role reached the app's database: %q %v", out, err)
	}
	for _, guess := range []string{password, "postgres", ""} {
		if out, err := psql("postgres", guess, "postgres", "select 1"); err == nil {
			t.Errorf("the server's superuser logged in over the network with %q: %s", guess, out)
		}
	}
}

// The servers AppTown starts beside its own steps make the app's user a
// superuser (the kind's stepEnv), so test runners that make their own
// databases or load fixtures work; a server without the step environment
// does not. Both are started as the tests step's runner starts them.
func TestIntegrationOnlyAStepsPostgresMakesTheAppASuperuser(t *testing.T) {
	local := stockPostgres(t).ResolveLocal(nil, "", nil)
	for _, c := range []struct {
		name    string
		service LocalService
		check   string
	}{
		{"a step's server", local.ForSteps(), `psql -tAc 'select rolsuper from pg_roles where rolname = current_user' | grep -qx t && psql -c 'create database test_app'`},
		{"any other server", *local, `psql -tAc 'select rolsuper from pg_roles where rolname = current_user' | grep -qx f && ! psql -c 'create database test_app'`},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
			defer cancel()
			out := (execution.Runner{}).Run(ctx, execution.Docker, execution.Request{
				Argv:  []string{"sh", "-c", "set -e; " + c.check},
				Image: c.service.Image, Network: "none",
				Env:      []string{"PGHOST=postgres", "PGUSER=app", "PGDATABASE=app", "PGPASSWORD=" + c.service.Env["POSTGRES_PASSWORD"]},
				Name:     execution.UniqueName("forge-test-pg-step"),
				Timeout:  4 * time.Minute,
				Services: []deploy.ServiceSpec{c.service.Spec()},
			})
			if !out.Exited0() || out.RunErr != nil {
				t.Fatalf("%s: %v %s", c.name, out.RunErr, out.Output)
			}
		})
	}
}
