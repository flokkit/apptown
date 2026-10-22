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

// The declared commands recover actual application rows as the app user. The
// verifier rejects missing/extra rows, damaged copies and altered DDL, including
// column definitions exchanged between two empty tables (global sorting of SQL
// would incorrectly accept that change).
func TestIntegrationMariaDBLogicalRecoveryPreservesSchemaAndRows(t *testing.T) {
	raw, err := stock.FS.ReadFile("dependencies/mariadb.yaml")
	if err != nil {
		t.Fatal(err)
	}
	kind, err := Parse(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	cp := kind.Copy
	image := strings.ReplaceAll(cp.Client, "{version}", kind.Defaults["version"])
	name := fmt.Sprintf("forge-mariadb-copy-%d", os.Getpid())
	runDocker(t, nil, "network", "create", "--internal", name)
	t.Cleanup(func() { _ = dockertest.Run("network", "rm", name) })
	runDocker(t, nil, "run", "-d", "--name", name, "--network", name, "--memory", "256m", "-e", "MARIADB_ROOT_PASSWORD=fixture-admin", "-e", "MARIADB_DATABASE=app", "-e", "MARIADB_USER=app", "-e", "MARIADB_PASSWORD=fixture-app", image, "--innodb-buffer-pool-size=64M", "--max-connections=20", "--performance-schema=OFF")
	t.Cleanup(func() { _ = dockertest.Run("rm", "-f", "-v", name) })
	if !poll.Until(context.Background(), 90*time.Second, 250*time.Millisecond, func() bool {
		return dockertest.Run("exec", name, "healthcheck.sh", "--connect", "--innodb_initialized") == nil
	}) {
		t.Fatal("MariaDB did not become ready")
	}
	admin := func(sql string) {
		t.Helper()
		if out, err := dockertest.CombinedOutput("exec", name, "sh", "-c", `MYSQL_PWD="$MARIADB_ROOT_PASSWORD" mariadb -uroot -e "$1"`, "admin", sql); err != nil {
			t.Fatalf("fixture setup: %v %s", err, out)
		}
	}
	admin("CREATE DATABASE fresh; GRANT ALL ON fresh.* TO 'app'@'%'; CREATE DATABASE nothing; GRANT ALL ON nothing.* TO 'app'@'%';")
	run := func(db, port, command string, input []byte) ([]byte, error) {
		var out, errs bytes.Buffer
		_, err := (docker.CLI{}).Run(context.Background(), "", docker.Command{Args: []string{"run", "--rm", "-i", "--network", name, "-e", "DB_HOST=" + name, "-e", "DB_PORT=" + port, "-e", "DB_NAME=" + db, "-e", "DB_USER=app", "-e", "MYSQL_PWD", image, "sh", "-c", command}, Env: []string{"MYSQL_PWD=fixture-app"}, Stdin: bytes.NewReader(input), Stdout: &out, Stderr: &errs})
		if err != nil {
			return errs.Bytes(), err
		}
		return out.Bytes(), nil
	}
	sql := func(db, statement string) {
		t.Helper()
		if out, err := run(db, "3306", cp.Import, []byte(statement)); err != nil {
			t.Fatalf("fixture SQL: %v %s", err, out)
		}
	}
	// sql-guarantee: app-owned MariaDB fixture data/programs, not AppTown tables or domain setup.
	sql("app", "CREATE TABLE ledger (id INT PRIMARY KEY, body TEXT, bin BLOB) ENGINE=InnoDB; INSERT INTO ledger VALUES (1,'garden\\nnotes',X'00FF'),(2,'second',NULL); CREATE TABLE a(x INT); CREATE TABLE b(y INT); CREATE VIEW ledger_view AS SELECT id, body FROM ledger; CREATE TRIGGER ledger_note BEFORE INSERT ON ledger FOR EACH ROW SET NEW.body=CONCAT(NEW.body,'!');")
	// sql-guarantee: app-owned MariaDB fixture data/programs, not AppTown tables or domain setup.
	sql("app", "CREATE TABLE trace (message TEXT);\nDELIMITER ;;\nCREATE TRIGGER a_trace AFTER INSERT ON a FOR EACH ROW BEGIN\nINSERT INTO trace VALUES ('first');\nINSERT INTO trace VALUES ('second');\nEND;;\nDELIMITER ;\n")
	good, err := run("app", "3306", cp.Export, nil)
	if err != nil || len(good) == 0 {
		t.Fatalf("export: %v %s", err, good)
	}
	if out, err := run("fresh", "3306", cp.Import, good); err != nil {
		t.Fatalf("import: %v %s", err, out)
	}
	verifies := func(db string, copy []byte, want bool) {
		t.Helper()
		out, err := run(db, "3306", cp.Verify, copy)
		if (err == nil) != want {
			t.Fatalf("verify %s wanted %t: %v %s", db, want, err, out)
		}
	}
	verifies("fresh", good, true)
	if out, err := run("fresh", "3306", `mariadb --host="$DB_HOST" --user="$DB_USER" -N "$DB_NAME" -e 'SELECT SUM(id),HEX(bin) FROM ledger GROUP BY bin ORDER BY SUM(id)'`, nil); err != nil || !strings.Contains(string(out), "00FF") {
		t.Fatalf("restored actual rows: %v %s", err, out)
	}
	// Same INSERT line multiset, different trigger program: order matters.
	// sql-guarantee: app-owned MariaDB fixture data/programs, not AppTown tables or domain setup.
	sql("fresh", "DROP TRIGGER a_trace;\nDELIMITER ;;\nCREATE TRIGGER a_trace AFTER INSERT ON a FOR EACH ROW BEGIN\nINSERT INTO trace VALUES ('second');\nINSERT INTO trace VALUES ('first');\nEND;;\nDELIMITER ;\n")
	verifies("fresh", good, false)
	if out, err := run("fresh", "3306", cp.Restore, good); err != nil {
		t.Fatalf("restore trigger: %v %s", err, out)
	}
	verifies("fresh", good, true)
	verifies("nothing", good, false)
	verifies("fresh", nil, false)
	verifies("fresh", []byte("not SQL"), false)
	verifies("fresh", good[:len(good)/2], false)
	if out, err := run("fresh", "1", cp.Verify, good); err == nil {
		t.Fatalf("unreachable database verified: %s", out)
	}
	// sql-guarantee: app-owned MariaDB fixture data/programs, not AppTown tables or domain setup.
	sql("fresh", "DELETE FROM ledger WHERE id=1;")
	verifies("fresh", good, false)
	if out, err := run("fresh", "3306", cp.Restore, good); err != nil {
		t.Fatalf("restore: %v %s", err, out)
	}
	verifies("fresh", good, true)
	// sql-guarantee: app-owned MariaDB fixture data/programs, not AppTown tables or domain setup.
	sql("fresh", "INSERT INTO ledger VALUES (3,'extra',NULL);")
	if out, err := run("fresh", "3306", `mariadb --host="$DB_HOST" --user="$DB_USER" -N "$DB_NAME" -e 'SELECT body FROM ledger_view WHERE id=3'`, nil); err != nil || strings.TrimSpace(string(out)) != "extra!" {
		t.Fatalf("restored view and trigger must work: %v %s", err, out)
	}
	verifies("fresh", good, false)
	if out, err := run("fresh", "3306", cp.Restore, good); err != nil {
		t.Fatalf("restore: %v %s", err, out)
	}
	sql("fresh", "ALTER TABLE a CHANGE x y INT; ALTER TABLE b CHANGE y x INT;")
	verifies("fresh", good, false)
	sql("fresh", "CREATE TABLE unexpected (id INT);")
	if out, err := run("fresh", "3306", cp.Restore, good); err != nil {
		t.Fatalf("restore: %v %s", err, out)
	}
	verifies("fresh", good, false) // documented extra-object limitation remains visible
}
