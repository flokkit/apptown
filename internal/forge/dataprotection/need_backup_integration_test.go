//go:build integration

package dataprotection

import (
	"context"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/deploy/docker/dockertest"
	"flokkit.io/forge/internal/poll"
)

// On a real daemon with the stock Redis declaration: a backup holds a write
// made a moment before it (the declared SAVE, not the last snapshot on
// disk) and the keys of every logical database, and restoring that set into
// another deployment's Redis leaves Redis serving the data, confirmed by the
// declared check.
func TestIntegrationRedisBackupRestoresIntoAFreshDeployment(t *testing.T) {
	s, pid, redisOf := needWorld(t, "redis")
	for _, c := range redisOf {
		waitForPong(t, c)
	}

	// The fresh deployment holds something of its own, snapshotted; the
	// restore replaces it.
	cli(t, redisOf["fresh"], "SET stale 1", "OK")
	cli(t, redisOf["fresh"], "SAVE", "OK")
	// An earlier snapshot holds the first key; the second is written
	// after it and only a save at backup time can hold it.
	cli(t, redisOf["main"], "SET first 1", "OK")
	cli(t, redisOf["main"], "SAVE", "OK")
	cli(t, redisOf["main"], "SET second 2", "OK")
	// Apps keep sessions or queues in other logical databases; the copy
	// and its check cover all of them.
	cli(t, redisOf["main"], "-n 3 SET third 3", "OK")
	b, err := s.TakeBackup(context.Background(), pid, "main", "manual", "bls")
	if err != nil || b.Status != BackupOK || len(b.Items) != 1 || b.Items[0].Copy != CopyDeclared {
		t.Fatalf("take: %+v %v", b, err)
	}

	res, err := s.restoreBackup(context.Background(), nil, pid, "fresh", b.ID, "bls")
	if err != nil || res.Status != RestoreDone || len(res.Items) != 1 || res.Items[0].Outcome != RestoreVerified {
		t.Fatalf("restore: %+v %v", res, err)
	}
	cli(t, redisOf["fresh"], "GET first", "1")
	cli(t, redisOf["fresh"], "GET second", "2")
	cli(t, redisOf["fresh"], "-n 3 GET third", "3")
	cli(t, redisOf["fresh"], "EXISTS stale", "0")
}

// waitForPong polls a just-started Redis until it answers, with a deadline.
func waitForPong(t *testing.T, container string) {
	t.Helper()
	var out []byte
	if !poll.Until(context.Background(), 30*time.Second, 100*time.Millisecond, func() bool {
		out, _ = dockertest.CombinedOutput("exec", container, "redis-cli", "PING")
		return strings.TrimSpace(string(out)) == "PONG"
	}) {
		t.Fatalf("redis in %s did not answer: %q", container, out)
	}
}

// cli runs one redis-cli command in a container and wants its answer.
func cli(t *testing.T, container, command, want string) {
	t.Helper()
	out, err := dockertest.CombinedOutput(append([]string{"exec", container, "redis-cli"}, strings.Fields(command)...)...)
	if err != nil || strings.TrimSpace(string(out)) != want {
		t.Fatalf("redis-cli %s in %s: %q %v (want %q)", command, container, out, err, want)
	}
}

// On a real daemon with the stock Postgres declaration: a backup is the
// kind's copy.export run in the database's own container, and restoring it
// into another deployment's Postgres puts it back in place by copy.restore
// and is confirmed there by copy.verify, both given the connection that
// container serves: the restore reads "verified", and the rows are there.
func TestIntegrationPostgresBackupRestoresInPlaceAndIsVerified(t *testing.T) {
	s, pid, pgOf := needWorld(t, "postgres")
	for _, c := range pgOf {
		waitForPostgres(t, c)
	}
	// sql-guarantee: the app's own database in its test container, not the forge's; the data a backup copies.
	psql(t, pgOf["main"], "CREATE TABLE ledger (n int); INSERT INTO ledger VALUES (41), (42)")
	// sql-guarantee: the app's own database in its test container, not the forge's; what the restore replaces.
	psql(t, pgOf["fresh"], "CREATE TABLE ledger (n int); INSERT INTO ledger VALUES (7)")
	b, err := s.TakeBackup(context.Background(), pid, "main", "manual", "bls")
	if err != nil || b.Status != BackupOK || len(b.Items) != 1 || b.Items[0].Format != "pg-custom" {
		t.Fatalf("take: %+v %v", b, err)
	}
	res, err := s.restoreBackup(context.Background(), nil, pid, "fresh", b.ID, "bls")
	if err != nil || res.Status != RestoreDone || len(res.Items) != 1 || res.Items[0].Outcome != RestoreVerified {
		t.Fatalf("restore: %+v %v", res, err)
	}
	if got := psql(t, pgOf["fresh"], "SELECT string_agg(n::text, ',' ORDER BY n) FROM ledger"); got != "41,42" {
		t.Fatalf("the restored database holds %q, want the copy's rows 41,42", got)
	}
}

// waitForPostgres polls a just-started Postgres until it accepts
// connections over TCP, with a deadline: the image's first-start server
// listens on its socket only, and stops once its setup is done.
func waitForPostgres(t *testing.T, container string) {
	t.Helper()
	var out []byte
	if !poll.Until(context.Background(), 60*time.Second, 200*time.Millisecond, func() bool {
		var err error
		out, err = dockertest.CombinedOutput("exec", container, "pg_isready", "-h", "127.0.0.1", "-U", "app", "-d", "app")
		return err == nil
	}) {
		t.Fatalf("postgres in %s did not answer: %q", container, out)
	}
}

// psql runs one statement in a container's database as its user and
// returns the answer.
func psql(t *testing.T, container, sql string) string {
	t.Helper()
	out, err := dockertest.CombinedOutput("exec", container, "psql", "-U", "app", "-d", "app", "-v", "ON_ERROR_STOP=1", "-tA", "-c", sql)
	if err != nil {
		t.Fatalf("psql %s in %s: %q %v", sql, container, out, err)
	}
	return strings.TrimSpace(string(out))
}
