//go:build integration

package dataprotection

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/ids"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy/docker/dockertest"
	"github.com/flokkit/apptown/internal/poll"
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
	b, err := s.TakeBackup(context.Background(), pid, "main", "bls")
	if err != nil || b.Status != BackupOK || len(b.Items) != 1 || b.Items[0].Copy != CopyDeclared {
		t.Fatalf("take: %+v %v", b, err)
	}

	res, err := s.restoreBackup(context.Background(), nil, pid, "fresh", b.ID, "bls", false)
	if err != nil || res.Status != restoreDone || len(res.Items) != 1 || res.Items[0].Outcome != restoreVerified {
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
	// sql-guarantee: the app's own database in its test container, not AppTown's; the data a backup copies.
	psql(t, pgOf["main"], "CREATE TABLE ledger (n int); INSERT INTO ledger VALUES (41), (42)")
	// sql-guarantee: the app's own database in its test container, not AppTown's; what the restore replaces.
	psql(t, pgOf["fresh"], "CREATE TABLE ledger (n int); INSERT INTO ledger VALUES (7)")
	b, err := s.TakeBackup(context.Background(), pid, "main", "bls")
	if err != nil || b.Status != BackupOK || len(b.Items) != 1 || b.Items[0].Format != "pg-custom" {
		t.Fatalf("take: %+v %v", b, err)
	}
	res, err := s.restoreBackup(context.Background(), nil, pid, "fresh", b.ID, "bls", false)
	if err != nil || res.Status != restoreDone || len(res.Items) != 1 || res.Items[0].Outcome != restoreVerified {
		t.Fatalf("restore: %+v %v", res, err)
	}
	if got := psql(t, pgOf["fresh"], "SELECT string_agg(n::text, ',' ORDER BY n) FROM ledger"); got != "41,42" {
		t.Fatalf("the restored database holds %q, want the copy's rows 41,42", got)
	}
}

// waitForPostgres waits, with a deadline, until the need passes its own
// health check, which is what a release's boot waits for
// (EnsureStandingServices starts a need and does not wait). Answering on
// TCP is not enough: the stock start script takes connections before it
// has made the app's user the owner of its schema, and only marks itself
// ready after (22 Oct: a table made in that gap was refused, "permission
// denied for schema public").
func waitForPostgres(t *testing.T, container string) {
	t.Helper()
	var out []byte
	if !poll.Until(context.Background(), 2*time.Minute, 200*time.Millisecond, func() bool {
		out, _ = dockertest.CombinedOutput("inspect", "-f", "{{.State.Health.Status}}", container)
		return strings.TrimSpace(string(out)) == "healthy"
	}) {
		logs, _ := dockertest.CombinedOutput("logs", "--tail", "40", container)
		t.Fatalf("postgres in %s did not become healthy: %q\n%s", container, out, logs)
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

// Composed coverage: restore the logical pre-release copy, then reconcile an
// earlier bundle to return its rows, app code and same-line engine build.
func TestIntegrationBeforeReleaseRestoresRowsCodeAndEarlierPostgres(t *testing.T) {
	const older = "postgres:16.15-alpine@sha256:aa6cb33cf4c19c765cefcf48d96176258800870a9a1e930ad126200f9d825139"
	const newer = "postgres:16.16-alpine@sha256:f2f98726820d0342055351c6e9d36e705f5bfffb3dd53ff556325114b08afb6d"
	s, pid, pg := needWorld(t, "postgres", older)
	waitForPostgres(t, pg["main"])
	dep, err := s.Deployments.ByName(t.Context(), pid, "main")
	if err != nil {
		t.Fatal(err)
	}
	target, err := s.targets.ByID(t.Context(), dep.TargetID)
	if err != nil {
		t.Fatal(err)
	}
	names := hostNamesOf(t, dep, "app")
	t.Cleanup(func() { _ = dockertest.Run("rm", "-f", names.App()) })
	var url string
	launch := func(revision, engine, code string, backup ids.ID) *deployment.Release {
		t.Helper()
		dep, err = s.Deployments.ByName(t.Context(), pid, "main")
		if err != nil {
			t.Fatal(err)
		}
		command := "while true; do printf 'HTTP/1.1 200 OK\\r\\nContent-Length: 3\\r\\nConnection: close\\r\\n\\r\\n" + code + "' | nc -l -p 8080; done"
		yaml := fmt.Sprintf("app:\n  web:\n    image: %s\n    ports: ['8080']\n    public: true\n    command: [sh, -c, %q]\nneeds: {postgres: {}}\n", older, command)
		c, err := appcontract.Parse([]byte(yaml))
		if err != nil {
			t.Fatal(err)
		}
		src := s.sources.(*releasedContracts)
		src.mu.Lock()
		src.by[revision] = yaml
		src.mu.Unlock()
		bundle, err := s.Bundles.Cut(t.Context(), pid, revision, map[string]string{"web": older}, nil, []byte(yaml), deployment.BundleOrigin{TargetID: target.ID, Needs: map[string]string{"postgres": engine}})
		if err != nil {
			t.Fatal(err)
		}
		rel, err := s.releases.Start(t.Context(), deployment.ReleaseDraft{Source: deployment.ReleaseFromBundle, ProjectID: pid, DeploymentID: dep.ID, Deployment: dep.Name, BundleID: bundle, Revision: revision, Actor: "bls"}, revision)
		if err != nil {
			t.Fatal(err)
		}
		body, err := json.Marshal(docker.SourceInputs{Version: 1, Revision: revision})
		if err != nil {
			t.Fatal(err)
		}
		art := deploy.Artifact{Kind: "docker-source", Body: body, Hash: deploy.ConfigHash(string(body)), Source: &deploy.Source{Slug: "app", Workspace: t.TempDir(), Revision: revision, Contract: c, Images: map[string]string{"web": older}, Needs: map[string]string{"postgres": engine}}}
		at, _, err := s.Outcomes.Admit(t.Context(), deployment.Start{DeploymentID: dep.ID, TargetID: target.ID, Key: revision, Inputs: deployment.Inputs{ReleaseID: rel.ID, BundleID: bundle, Revision: revision, ArtifactHash: art.Hash, DockerSource: string(body), Backup: backup}})
		if err != nil {
			t.Fatal(err)
		}
		driver, err := s.Service.providers.Driver(t.Context(), target, dep)
		if err != nil {
			t.Fatal(err)
		}
		driver.(*docker.Driver).URL = func(_ string, _ string, port int) (string, error) {
			if port == 0 {
				port = 8080
			}
			return fmt.Sprintf("http://127.0.0.1:%d", port), nil
		}
		observed, err := driver.Reconcile(t.Context(), dep.Placement(), at.Generation, art, revision, func(string, ...any) {}, s.Outcomes.Checkpoint(t.Context(), at))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Outcomes.Complete(t.Context(), deployment.Completion{Attempt: at, Observed: observed}); err != nil {
			t.Fatal(err)
		}
		url = observed.URL
		out, err := s.releases.ByID(t.Context(), rel.ID)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	first := launch("earlier", older, "old", ids.Nil)
	// sql-guarantee: external application rows are the restore's observed result.
	psql(t, pg["main"], "CREATE TABLE ledger(n int); INSERT INTO ledger VALUES(41)")
	copy, err := s.BeforeRelease(t.Context(), pid, "main", false)
	if err != nil || !copy.Complete() || copy.copy == nil {
		t.Fatalf("pre-release copy: %+v %v", copy, err)
	}
	moved := launch("later", newer, "new", copy.copy.ID)
	// sql-guarantee: these later rows must be removed by the logical restore.
	psql(t, pg["main"], "INSERT INTO ledger VALUES(42)")
	selected, err := s.ReleaseBefore(t.Context(), pid, "main", copy.copy.ID)
	if err != nil || selected != first.Number {
		t.Fatalf("before %d: %d %v", moved.Number, selected, err)
	}
	if _, err := s.restoreBackup(t.Context(), nil, pid, "main", copy.copy.ID, "bls", true); err != nil {
		t.Fatal(err)
	}
	launch("earlier-again", older, "old", ids.Nil)
	if got := psql(t, pg["main"], "SELECT string_agg(n::text,',' ORDER BY n) FROM ledger"); got != "41" {
		t.Fatalf("restored rows: %q", got)
	}
	response, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	code, err := io.ReadAll(response.Body)
	if err != nil || string(code) != "old" {
		t.Fatalf("restored code: %q %v", code, err)
	}
	dep, err = s.Deployments.ByName(t.Context(), pid, "main")
	if err != nil {
		t.Fatal(err)
	}
	binding, found, err := s.Deployments.StorageOf(dep).Get(t.Context(), "postgres", names.NeedVolume("postgres"))
	if err != nil || !found || binding.DeclaredImage != older {
		t.Fatalf("earlier engine not restored: %+v %v", binding, err)
	}
}
