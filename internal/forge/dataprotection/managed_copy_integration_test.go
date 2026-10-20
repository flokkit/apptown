//go:build integration

package dataprotection

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/deploy/docker/dockertest"
	"flokkit.io/forge/internal/forge/deployment"
	"flokkit.io/forge/internal/forge/operations"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/poll"
)

// managedPostgres is a real Postgres that stands in for DigitalOcean's
// managed clusters: TLS on, an admin, and the app's own user, which is
// neither a superuser nor allowed to make databases or roles. Each database
// the fake account makes, removes or deletes with its cluster is made,
// removed or dropped here; a new one is handed to the app's user as the
// provider's admin connection does.
type managedPostgres struct {
	t         *testing.T
	container string
	host      string
}

func startManagedPostgres(t *testing.T) *managedPostgres {
	t.Helper()
	crt, key := selfSigned(t)
	name := fmt.Sprintf("forge-managed-pg-%d", os.Getpid())
	_ = dockertest.Run("rm", "-f", "-v", name)
	// Only TLS connections from outside the container are let in, as on a
	// managed cluster.
	start := `printf '%s' "$CRT" > /tmp/s.crt && printf '%s' "$KEY" > /tmp/s.key && printf 'local all all trust\nhostssl all all all scram-sha-256\n' > /tmp/hba.conf && ` +
		`chown postgres /tmp/s.crt /tmp/s.key /tmp/hba.conf && chmod 600 /tmp/s.key && ` +
		`exec docker-entrypoint.sh postgres -c ssl=on -c ssl_cert_file=/tmp/s.crt -c ssl_key_file=/tmp/s.key -c hba_file=/tmp/hba.conf`
	if out, err := (docker.CLI{}).Run(context.Background(), "", docker.Command{
		Args: []string{"run", "-d", "--name", name, "-e", "POSTGRES_PASSWORD=admin", "-e", "CRT", "-e", "KEY", "--entrypoint", "sh", "postgres:16-alpine", "-c", start},
		Env:  []string{"CRT=" + crt, "KEY=" + key},
	}); err != nil {
		t.Fatalf("starting the stand-in Postgres: %v %s", err, out)
	}
	t.Cleanup(func() { _ = dockertest.Run("rm", "-f", "-v", name) })
	pg := &managedPostgres{t: t, container: name}
	pg.admin("CREATE ROLE app LOGIN PASSWORD 'pw-app' NOSUPERUSER NOCREATEDB NOCREATEROLE")
	// Every DigitalOcean cluster has defaultdb, which any user may connect to.
	pg.admin("CREATE DATABASE defaultdb")
	out, err := dockertest.Output("inspect", "-f", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}", name)
	if err != nil || strings.TrimSpace(string(out)) == "" {
		t.Fatalf("the stand-in Postgres has no address: %v %s", err, out)
	}
	pg.host = strings.TrimSpace(string(out))
	return pg
}

// admin runs SQL as the cluster's admin, waiting for the server to come up.
func (pg *managedPostgres) admin(sql string, db ...string) string {
	pg.t.Helper()
	args := []string{"exec", pg.container, "psql", "-U", "postgres", "-v", "ON_ERROR_STOP=1", "-tA", "-c", sql}
	if len(db) > 0 {
		args = append(args, db[0])
	}
	var out []byte
	var err error
	// Until it answers, or fails for a reason other than still starting.
	// The image's entrypoint initialises the database on a temporary server
	// and stops it before starting the real one: "shutting down" is part
	// of starting.
	poll.Until(context.Background(), 60*time.Second, 300*time.Millisecond, func() bool {
		out, err = dockertest.CombinedOutput(args...)
		starting := false
		for _, s := range []string{"the database system is starting up", "the database system is shutting down", "could not connect", "No such file or directory"} {
			starting = starting || strings.Contains(string(out), s)
		}
		return err == nil || !starting
	})
	if err != nil {
		pg.t.Fatalf("admin %q: %v %s", sql, err, out)
	}
	return strings.TrimSpace(string(out))
}

// hook makes the fake account's database operations real.
func (pg *managedPostgres) hook(w *dataWorld) func(op, cluster, name string) error {
	return func(op, cluster, name string) error {
		switch op {
		case "create":
			pg.admin(fmt.Sprintf("CREATE DATABASE %q", name))
		case "drop":
			pg.admin(fmt.Sprintf("DROP DATABASE IF EXISTS %q WITH (FORCE)", name))
		case "delete-cluster":
			for _, db := range w.api.Snapshot().ClusterDBs[cluster] {
				if db != "defaultdb" {
					pg.admin(fmt.Sprintf("DROP DATABASE IF EXISTS %q WITH (FORCE)", db))
				}
			}
		}
		return nil
	}
}

func (pg *managedPostgres) own(_ context.Context, _ string, db, user string) error {
	pg.admin(fmt.Sprintf("ALTER DATABASE %q OWNER TO %q", db, user))
	return nil
}

// asApp runs SQL as the app's user over TLS, as the app does.
func (pg *managedPostgres) asApp(db, sql string) string {
	pg.t.Helper()
	out, err := (docker.CLI{}).Run(context.Background(), "", docker.Command{
		Args: []string{"run", "--rm", "-e", "PGPASSWORD", "-e", "PGSSLMODE=require", "postgres:16-alpine",
			"psql", "-h", pg.host, "-U", "app", "-d", db, "-v", "ON_ERROR_STOP=1", "-tA", "-c", sql},
		Env: []string{"PGPASSWORD=pw-app"},
	})
	if err != nil {
		pg.t.Fatalf("as the app on %s, %q: %v %s", db, sql, err, out)
	}
	return strings.TrimSpace(string(out))
}

func selfSigned(t *testing.T) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "stand-in"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kd, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kd}))
}

// The round trip with the stock Postgres copy against a real server, as on
// a managed cluster: TLS required, the app's own user (not a superuser,
// unable to make databases), the provider making each new database and
// handing it over. Records the app wrote before a production deployment is
// taken down are in its final copy, the copy comes back into a new database
// before the cluster goes, and a new deployment started from it serves the
// same records, read as the app reads them, in tables its user owns.
func TestIntegrationTheRoundTripKeepsTheRecords(t *testing.T) {
	pg := startManagedPostgres(t)
	var w *dataWorld
	w = newDataWorldWith(t, dataWorldOptions{docker: docker.CLI{Binary: "docker"}, ownDatabase: pg.own})
	w.api.OnDatabase(pg.hook(w))
	w.api.ServeClustersAt(pg.host, 5432)
	w.protect()
	ctx := context.Background()
	if _, err := w.bind(w.project, "prod", "production", ids.Nil); err != nil {
		t.Fatal(err)
	}
	if rel, log := w.release("prod"); rel.State != deployment.Released {
		t.Fatalf("release %s:\n%s", rel.State, log)
	}
	// The app's migrations and records, as its user.
	// sql-guarantee: the app's own records on the stand-in cluster, not AppTown's database.
	pg.asApp("app", "create table ledger (n int primary key, note text); insert into ledger values (41, 'first'), (42, 'second')")

	appDatabases := func() string { return pg.admin("select count(*) from pg_database where datname='app'") }
	// Someone keeps data in defaultdb through the admin connection, of each
	// kind PostgreSQL stores outside an ordinary table too: the cluster is
	// not deleted while it holds any of it. A data-free
	// extension does not hold it up.
	kept := []struct{ what, make, clear string }{
		// sql-guarantee: another tenant's data on the stand-in cluster, not AppTown's database.
		{"a table in another schema", "create schema private; create table private.ledger (n int); insert into private.ledger values (7); revoke all on schema private from public", "drop schema private cascade"},
		{"a materialized view", "create materialized view saved_report as select 7 as n", "drop materialized view saved_report"},
		{"a large object", "select lo_from_bytea(12345, convert_to('uncopied payload', 'UTF8'))", "select lo_unlink(12345)"},
		{"a sequence with a value", "create sequence invoices start 1001; select nextval('invoices')", "drop sequence invoices"},
		{"a view", "create view open_orders as select 1 as n", "drop view open_orders"},
	}
	pg.admin("create extension if not exists hstore", "defaultdb")
	for _, k := range kept {
		// sql-guarantee: another tenant's data on the stand-in cluster, not AppTown's database.
		pg.admin(k.make, "defaultdb")
		res, err := w.s.TakeDown(ctx, RemoveDeployment{ProjectID: w.project, Name: "prod"})
		if err != nil || res.Operation == nil {
			t.Fatalf("%s: take-down: %+v %v", k.what, res, err)
		}
		if op := w.run(res.Operation); op.State != operations.Failed || !strings.Contains(op.Detail, "defaultdb (not shown empty") {
			t.Errorf("take-down with %s in defaultdb %s: %s", k.what, op.State, op.Detail)
		}
		if got := appDatabases(); got != "1" {
			t.Fatalf("the database went while defaultdb held %s: %s", k.what, got)
		}
		pg.admin(k.clear, "defaultdb")
	}
	res, err := w.s.TakeDown(ctx, RemoveDeployment{ProjectID: w.project, Name: "prod"})
	if err != nil || res.Operation == nil {
		t.Fatalf("take-down again: %+v %v", res, err)
	}
	op := w.run(res.Operation)
	if !endedAs(op, OperationVerified) {
		t.Fatalf("take-down %s: %s\n%+v", op.State, op.Detail, op.Steps)
	}
	if got := appDatabases(); got != "0" {
		t.Fatalf("the old database is still there after the cluster went: %s", got)
	}
	finals, err := w.s.FinalCopies(ctx, w.project)
	// Each attempt took a final copy; the newest is the one that came back
	// before the cluster went.
	if err != nil || len(finals) != len(kept)+1 || !finals[0].Verified {
		t.Fatalf("final copies: %+v %v", finals, err)
	}
	if _, err := w.bind(w.project, "prod", "production", finals[0].Backup); err != nil {
		t.Fatal(err)
	}
	rel, log := w.release("prod")
	if rel.State != deployment.Released {
		t.Fatalf("release from the copy %s:\n%s", rel.State, log)
	}
	if got := pg.asApp("app", "select string_agg(n || ':' || note, ',' order by n) from ledger"); got != "41:first,42:second" {
		t.Fatalf("the records through the new database: %q", got)
	}
	if owner := pg.admin("select pg_get_userbyid(relowner) from pg_class where relname='ledger'", "app"); owner != "app" {
		t.Fatalf("the restored table is owned by %q, not the app's user", owner)
	}
}
