//go:build integration

package dependency

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/docker/dockertest"
	"github.com/flokkit/apptown/internal/poll"
	"github.com/flokkit/apptown/stock"
)

// The stock Postgres copy, run as a copy runner runs it: each command in a
// throwaway client container given the app user's connection, against a
// server on which that user owns its database and is neither a superuser
// nor allowed to make databases or roles, as on a managed cluster. A copy
// imported into a new, empty database verifies; an unreadable copy, an
// empty file, a copy that lists no table and an unreachable database each
// fail verification, so inability to check never reads as verified.
func TestIntegrationThePostgresCopyVerifiesOnlyAGoodCopy(t *testing.T) {
	raw, err := stock.FS.ReadFile("dependencies/postgres.yaml")
	if err != nil {
		t.Fatal(err)
	}
	kind, err := Parse(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	cp := kind.Copy
	client := strings.ReplaceAll(cp.Client, "{version}", "16")
	net := fmt.Sprintf("forge-copy-test-%d", os.Getpid())
	server := net + "-pg"
	runDocker(t, nil, "network", "create", "--internal", net)
	t.Cleanup(func() { _ = dockertest.Run("network", "rm", net) })
	runDocker(t, nil, "run", "-d", "--name", server, "--network", net, "-e", "POSTGRES_PASSWORD=admin", client)
	t.Cleanup(func() { _ = dockertest.Run("rm", "-f", "-v", server) })
	admin := func(sql string) {
		t.Helper()
		var out []byte
		var err error
		if !poll.Until(context.Background(), 60*time.Second, 250*time.Millisecond, func() bool {
			out, err = dockertest.CombinedOutput("exec", server, "psql", "-U", "postgres", "-v", "ON_ERROR_STOP=1", "-c", sql)
			return err == nil
		}) {
			t.Fatalf("admin %q: %v %s", sql, err, out)
		}
	}
	admin("CREATE ROLE app LOGIN PASSWORD 'app' NOSUPERUSER NOCREATEDB NOCREATEROLE")
	admin("CREATE DATABASE app OWNER app")
	admin("CREATE DATABASE fresh OWNER app")
	admin("CREATE DATABASE nothing OWNER app")
	// run is one command of the copy in a client container as the app's
	// user on database db, port its port, with stdin.
	run := func(db, port, command string, stdin []byte) ([]byte, error) {
		var out, errs bytes.Buffer
		_, err := (docker.CLI{}).Run(context.Background(), "", docker.Command{
			Args: []string{"run", "--rm", "-i", "--network", net,
				"-e", "PGHOST=" + server, "-e", "PGPORT=" + port, "-e", "PGUSER=app", "-e", "PGPASSWORD", "-e", "PGDATABASE=" + db, "-e", "PGSSLMODE=disable",
				client, "sh", "-c", command},
			Env:   []string{"PGPASSWORD=app"},
			Stdin: bytes.NewReader(stdin), Stdout: &out, Stderr: &errs,
		})
		if err != nil {
			return errs.Bytes(), err
		}
		return out.Bytes(), nil
	}
	// sql-guarantee: the app's own records on a stand-in server, not AppTown's database.
	if out, err := run("app", "5432", "psql -v ON_ERROR_STOP=1 -c 'create table ledger (n int); insert into ledger values (41), (42)'", nil); err != nil {
		t.Fatalf("seeding: %v %s", err, out)
	}
	good, err := run("app", "5432", cp.Export, nil)
	if err != nil || len(good) == 0 {
		t.Fatalf("export: %v %s", err, good)
	}
	none, err := run("nothing", "5432", cp.Export, nil)
	if err != nil {
		t.Fatalf("export of an empty database: %v %s", err, none)
	}
	if out, err := run("fresh", "5432", cp.Import, good); err != nil {
		t.Fatalf("import into a new, empty database: %v %s", err, out)
	}
	if out, err := run("fresh", "5432", cp.Verify, good); err != nil {
		t.Fatalf("the good copy must verify: %v %s", err, out)
	}
	if out, err := run("fresh", "5432", "psql -tA -c 'select sum(n) from ledger'", nil); err != nil || strings.TrimSpace(string(out)) != "83" {
		t.Fatalf("the rows came back: %v %q", err, out)
	}
	for name, tc := range map[string]struct {
		db, port string
		copy     []byte
	}{
		"an unreadable copy":         {"fresh", "5432", []byte("not a postgres archive")},
		"an empty file":              {"fresh", "5432", nil},
		"a copy that lists no table": {"fresh", "5432", none},
		"an unreachable database":    {"fresh", "1", good},
		"a database missing a table": {"nothing", "5432", good},
	} {
		if out, err := run(tc.db, tc.port, cp.Verify, tc.copy); err == nil {
			t.Errorf("%s verified: %s", name, out)
		}
	}
}

func runDocker(t *testing.T, stdin []byte, args ...string) {
	t.Helper()
	if out, err := (docker.CLI{}).Run(context.Background(), "", docker.Command{Args: args, Stdin: bytes.NewReader(stdin)}); err != nil {
		t.Fatalf("docker %s: %v %s", strings.Join(args, " "), err, out)
	}
}
