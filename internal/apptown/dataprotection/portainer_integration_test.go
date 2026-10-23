//go:build integration

package dataprotection

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/dependency"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/docker/dockertest"
	"github.com/flokkit/apptown/internal/deploy/portainer"
	"github.com/flokkit/apptown/internal/poll"
	"github.com/flokkit/apptown/stock"
)

// portainerCE is the Portainer the test runs, on the local Docker.
const portainerCE = "portainer/portainer-ce:2.27.9"

// localPortainer starts a Portainer CE on this Docker with one environment
// (the Docker socket) and an administrator's API key: what an operator
// attaches. It answers the URL and the key.
func localPortainer(t *testing.T, name string) (string, string) {
	t.Helper()
	if out, err := dockertest.CombinedOutput("run", "-d", "--name", name, "-p", "127.0.0.1::9000", "-v", "/var/run/docker.sock:/var/run/docker.sock", portainerCE); err != nil {
		t.Fatalf("starting Portainer: %v %s", err, out)
	}
	t.Cleanup(func() { _ = dockertest.Run("rm", "-f", name) })
	port, err := dockertest.Output("port", name, "9000/tcp")
	if err != nil {
		t.Fatal(err)
	}
	base := "http://" + strings.TrimSpace(strings.Split(string(port), "\n")[0])
	call := func(method, path string, body any, auth string) map[string]any {
		t.Helper()
		var out map[string]any
		var raw []byte
		if !poll.Until(context.Background(), 60*time.Second, 500*time.Millisecond, func() bool {
			req, _ := http.NewRequest(method, base+path, strings.NewReader(mustJSON(t, body)))
			if auth != "" {
				req.Header.Set("Authorization", "Bearer "+auth)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return false
			}
			defer resp.Body.Close()
			raw = make([]byte, 1<<16)
			n, _ := resp.Body.Read(raw)
			raw = raw[:n]
			return resp.StatusCode < 300
		}) {
			t.Fatalf("Portainer %s %s: %s", method, path, raw)
		}
		_ = json.Unmarshal(raw, &out)
		return out
	}
	const password = "forge-integration-1"
	call("POST", "/api/users/admin/init", map[string]string{"Username": "admin", "Password": password}, "")
	jwt, _ := call("POST", "/api/auth", map[string]string{"username": "admin", "password": password}, "")["jwt"].(string)
	// The environment: Portainer's own Docker socket.
	req, _ := http.NewRequest("POST", base+"/api/endpoints", strings.NewReader("Name=lan&EndpointCreationType=1"))
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode >= 300 {
		t.Fatalf("making the environment: %v %v", resp, err)
	}
	key, _ := call("POST", "/api/users/1/tokens", map[string]string{"description": "forge", "password": password}, jwt)["rawAPIKey"].(string)
	return base, key
}

// noStackSecrets is a stack whose parts have no secrets.
type noStackSecrets struct{}

func (noStackSecrets) EnvFor(string) map[string]string { return nil }

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	if v == nil {
		return ""
	}
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// A deployment in a Portainer environment, released by name with the
// app's needs (the Portainer driver renders the stack, as a release by name
// does: deployment.portainerRelease), is backed up and restored by the
// Docker host's copies, through Portainer's Docker proxy: its Postgres by
// pg_dump, its object store through the S3 API, its data folder as files;
// restored, both needs are verified and their data is back; a completed
// init container and a worker someone stopped stay as they were while the
// running app is stopped and started again. Its take-down takes a final
// set, then deletes the stack and the volumes its deploy made, and keeps
// (copied) a volume that was there before its first deploy.
func TestIntegrationAPortainerDeploymentIsBackedUpRestoredAndTakenDown(t *testing.T) {
	ctx := context.Background()
	slug := fmt.Sprintf("pt%d", os.Getpid())
	url, key := localPortainer(t, "forge-it-portainer-"+slug)
	db := testDB(t)
	pid := createTestProject(t, db, slug, "portainer backup")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "secrets.env"), []byte("PORTAINER_URL="+url+"\nPORTAINER_KEY="+key+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog := dependency.NewCatalog(db, nil)
	if _, _, err := catalog.SyncStock(ctx, stock.FS, "dependencies"); err != nil {
		t.Fatal(err)
	}
	s := assembled(db, testRuntime{Scope: docker.Scope{Client: docker.CLI{Binary: "docker"}}, DataDir: dir, Needs: catalog, Secrets: secret.NewService(db, secret.Config{DataDir: dir}), ChownImage: "alpine:3", BuiltInTarget: "local"},
		deployment.Dependencies{Releases: deployment.ReleaseDependencies{Sources: &releasedContracts{}}})
	// The take-down is a person's act: alice may.
	s.authority = &grants{who: "alice", deny: map[string]bool{}}
	target, err := s.Targets.Put(ctx, &deployment.Target{Name: "lan", Kind: deployment.KindPortainer, Origin: deployment.OriginAttached, NeedsMode: "emulators", SecretsDelivery: "inject", Status: deployment.TargetReady,
		Address: map[string]any{"urlFrom": "PORTAINER_URL", "apiKeyFrom": "PORTAINER_KEY", "environment": "lan"}, Ingress: map[string]any{"domain": "lan"}})
	if err != nil {
		t.Fatal(err)
	}
	dep, err := s.Deployments.EnsureStanding(ctx, pid, "lan", target.ID, deployment.Attributes{}.WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	// The stack and its volumes are named by the deployment, as the
	// Portainer release names them.
	prefix, base := portainer.StackNames(dep.ID, dep.HostPrefix, slug)
	stack := portainer.StackName(prefix, base)
	t.Cleanup(func() {
		_ = dockertest.Run("rm", "-f", stack+"-app-1", stack+"-worker-1", stack+"-postgres-1", stack+"-s3-1", stack+"-init-migrate-1")
		for _, v := range []string{"apptown-data-" + base, "apptown-need-" + base + "-postgres", "apptown-need-" + base + "-s3"} {
			_ = dockertest.Run("volume", "rm", "-f", stack+"_"+v)
		}
		_ = dockertest.Run("network", "rm", stack+"_default")
	})
	// The app's data folder was there before the first deploy (an earlier
	// take-down kept it): compose reuses it, and it is not the deployment's.
	if out, err := dockertest.CombinedOutput("volume", "create", "--label", "com.docker.compose.project="+stack, "--label", "com.docker.compose.volume=apptown-data-"+base, stack+"_apptown-data-"+base); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	// The release by name: the bundle's images (public ones here), built for
	// the environment's platform, rendered and run by the Portainer driver.
	contract, err := appcontract.Parse([]byte(`app:
  web:
    public: true
    image: alpine:3
    command: ["sh", "-c", "trap 'exit 0' TERM; while true; do sleep 1; done"]
    ports: ["8080"]
    dataDir: /data
  worker:
    image: alpine:3
    command: ["sh", "-c", "trap 'exit 0' TERM; while true; do sleep 1; done"]
init:
  - name: migrate
    image: alpine:3
    command: ["true"]
needs:
  postgres: {}
  s3: {}
`))
	if err != nil {
		t.Fatal(err)
	}
	cfg := &portainer.Target{PortainerURL: url, PortainerKey: key, EnvName: "lan"}
	endpoint, err := portainer.EndpointID(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	platform, err := portainer.EndpointPlatform(ctx, cfg, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	d, err := s.providers.Driver(ctx, target, dep)
	if err != nil {
		t.Fatal(err)
	}
	src := deploy.Source{Slug: slug, Revision: "0123456789abcdef", Contract: contract, Published: true, Platform: platform,
		Images: map[string]string{"web": "alpine:3", "worker": "alpine:3"}, Init: map[string]string{"migrate": "alpine:3"}}
	art, err := d.(deploy.Renderer).Render(ctx, dep.Placement(), src, nil, noStackSecrets{}, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	art.Source = &src
	if _, err := d.Reconcile(ctx, dep.Placement(), 1, art, "release-1", t.Logf, func(map[string]any) error { return nil }); err != nil {
		t.Fatal(err)
	}
	app, worker, pg, store := stack+"-app-1", stack+"-worker-1", stack+"-postgres-1", stack+"-s3-1"
	for _, c := range []string{app, worker, pg, store} {
		if !poll.Until(ctx, 120*time.Second, time.Second, func() bool {
			out, _ := dockertest.Output("inspect", "-f", "{{.State.Running}}{{if .State.Health}} {{.State.Health.Status}}{{end}}", c)
			s := strings.TrimSpace(string(out))
			return s == "true" || s == "true healthy"
		}) {
			t.Fatalf("%s did not come up", c)
		}
	}
	// The data: a row, an object with its content type, a file.
	psql := func(sql string) string {
		out, err := dockertest.CombinedOutput("exec", pg, "psql", "-U", "app", "-d", "app", "-tA", "-c", sql)
		if err != nil {
			t.Fatalf("psql %s: %v %s", sql, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	// sql-guarantee: the app's own Postgres in the stack, not AppTown's records
	psql("create table notes (body text); insert into notes values ('kept row')")
	// The store answers the deployment's own key, as its app is given it.
	creds := s.Service.env.NeedCredentials(deployment.NeedSecretsScope(dep.ID))
	key, kerr := creds(ctx, "s3", "user")
	secretKey, serr := creds(ctx, "s3", "password")
	if kerr != nil || serr != nil {
		t.Fatal(kerr, serr)
	}
	s3 := func(args ...string) string {
		cmd := append([]string{"run", "--rm", "--network", stack + "_default", "curlimages/curl:8.22.0", "-sS", "-f", "--aws-sigv4", "aws:amz:us-east-1:s3", "--user", key + ":" + secretKey}, args...)
		out, err := dockertest.CombinedOutput(cmd...)
		if err != nil {
			t.Fatalf("s3 %v: %v %s", args, err, out)
		}
		return string(out)
	}
	s3("-X", "PUT", "-H", "Content-Type: image/png", "--data-binary", "kept object", "http://s3:9000/app/logo.png")
	if out, err := dockertest.CombinedOutput("exec", app, "sh", "-c", "echo kept file > /data/notes.txt"); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	// The worker is stopped by a person; the init container has completed.
	if out, err := dockertest.CombinedOutput("stop", worker); err != nil {
		t.Fatalf("%v %s", err, out)
	}

	b, err := s.TakeBackup(ctx, pid, "lan", "manual", "bls")
	if err != nil || b.Status != BackupOK || len(b.Items) != 3 {
		t.Fatalf("the set: %+v %v", b, err)
	}
	// The data changes, and the set is put back.
	// sql-guarantee: the app's own Postgres in the stack, not AppTown's records
	psql("delete from notes")
	s3("-X", "PUT", "-H", "Content-Type: text/plain", "--data-binary", "changed", "http://s3:9000/app/logo.png")
	_ = dockertest.Run("exec", app, "rm", "/data/notes.txt")
	res, err := s.restoreBackup(ctx, nil, pid, "lan", b.ID, "bls")
	if err != nil || res.Status != RestoreNotVerified {
		t.Fatalf("restore: %+v %v", res, err)
	}
	for _, it := range res.Items {
		if (it.Name == "postgres" || it.Name == "s3") != (it.Outcome == RestoreVerified) {
			t.Fatalf("what witnessed %s: %+v", it.Name, it)
		}
	}
	if got := psql("select body from notes"); got != "kept row" {
		t.Fatalf("the restored row: %q", got)
	}
	if got := s3("http://s3:9000/app/logo.png"); got != "kept object" {
		t.Fatalf("the restored object: %q", got)
	}
	if head := s3("-I", "http://s3:9000/app/logo.png"); !strings.Contains(strings.ToLower(head), "content-type: image/png") {
		t.Fatalf("the restored object's headers: %s", head)
	}
	if out, _ := dockertest.Output("exec", app, "cat", "/data/notes.txt"); strings.TrimSpace(string(out)) != "kept file" {
		t.Fatalf("the restored file: %q", out)
	}
	for c, want := range map[string]string{app: "true", worker: "false"} {
		if out, _ := dockertest.Output("inspect", "-f", "{{.State.Running}}", c); strings.TrimSpace(string(out)) != want {
			t.Fatalf("%s running %s after the restore, want %s", c, out, want)
		}
	}
	if out, _ := dockertest.Output("inspect", "-f", "{{.State.Status}}", stack+"-init-migrate-1"); strings.TrimSpace(string(out)) != "exited" {
		t.Fatalf("the completed init container was started again: %s", out)
	}

	removed, err := s.TakeDown(ctx, RemoveDeployment{ProjectID: pid, Name: "lan"})
	if err != nil || removed.Removed == nil || removed.Removed.FinalBackup == nil {
		t.Fatalf("take-down: %+v %v", removed, err)
	}
	for v, want := range map[string]bool{stack + "_apptown-need-" + base + "-postgres": false, stack + "_apptown-need-" + base + "-s3": false, stack + "_apptown-data-" + base: true} {
		if there, _ := (docker.Scope{Client: docker.CLI{Binary: "docker"}}).Exists(ctx, "volume", "inspect", v); there != want {
			t.Fatalf("after the take-down the volume %s is there %v, want %v", v, there, want)
		}
	}
	retained, err := s.Retained.ForProject(ctx, pid)
	found := false
	for _, r := range retained {
		if name, _ := r.Handle["name"].(string); name == stack+"_apptown-data-"+base {
			why, _ := r.Handle["why"].(string)
			found = strings.Contains(why, "holds a copy")
		}
	}
	if err != nil || !found {
		t.Fatalf("the kept volume is not retained with its copy: %+v %v", retained, err)
	}
}
