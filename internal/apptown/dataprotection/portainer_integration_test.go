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
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/docker/dockertest"
	"github.com/flokkit/apptown/internal/deploy/portainer"
	"github.com/flokkit/apptown/internal/ids"
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
	var dataVolume string
	t.Cleanup(func() {
		if out, err := dockertest.CombinedOutput("rm", "-f", "-v", name); err != nil {
			t.Errorf("removing fixture Portainer: %v %s", err, out)
			return
		}
		if dataVolume != "" {
			out, err := dockertest.Output("volume", "ls", "--filter", "name="+dataVolume, "--format", "{{.Name}}")
			if err != nil {
				t.Errorf("checking fixture data volume cleanup: %v", err)
				return
			}
			for _, remaining := range strings.Fields(string(out)) {
				if remaining == dataVolume {
					t.Error("fixture Portainer data volume remains after cleanup")
				}
			}
		}
	})
	volume, err := dockertest.Output("inspect", "--format", `{{range .Mounts}}{{if eq .Destination "/data"}}{{.Name}}{{end}}{{end}}`, name)
	if err != nil {
		t.Fatal(err)
	}
	dataVolume = strings.TrimSpace(string(volume))
	if dataVolume == "" {
		t.Fatal("fixture Portainer has no anonymous data volume to verify")
	}
	client := &http.Client{Timeout: 10 * time.Second}
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
			resp, err := client.Do(req)
			if err != nil {
				return false
			}
			defer resp.Body.Close()
			raw = make([]byte, 1<<16)
			n, _ := resp.Body.Read(raw)
			raw = raw[:n]
			return resp.StatusCode < 300
		}) {
			t.Fatalf("Portainer fixture %s %s did not succeed before deadline", method, path)
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
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("making the fixture environment: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		t.Fatalf("making the fixture environment: HTTP %d", resp.StatusCode)
	}
	key, _ := call("POST", "/api/users/1/tokens", map[string]string{"description": "forge", "password": password}, jwt)["rawAPIKey"].(string)
	return base, key
}

// Separate Portainer servers may report the same endpoint number and backing
// daemon. Neither value identifies the Portainer server's stack registry.
func TestIntegrationPortainerScopeObservationsAcrossConnections(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	name := fmt.Sprintf("apptown-it-scope-%d", time.Now().UnixNano())
	firstURL, firstKey := localPortainer(t, name+"-a")
	secondURL, secondKey := localPortainer(t, name+"-b")
	if firstURL == secondURL {
		t.Fatal("independent servers must have distinct API origins")
	}
	first := &portainer.Target{PortainerURL: firstURL, PortainerKey: firstKey, EnvName: "lan"}
	second := &portainer.Target{PortainerURL: secondURL, PortainerKey: secondKey, EnvName: "lan"}
	read := func(cfg *portainer.Target) (int64, string) {
		t.Helper()
		endpoint, err := portainer.EndpointID(ctx, cfg)
		if err != nil {
			t.Fatalf("resolving owned fixture endpoint: %v", err)
		}
		status, body, err := portainer.JSON(ctx, cfg, "GET", fmt.Sprintf("/api/endpoints/%d/docker/info", endpoint), nil)
		if err != nil || status != http.StatusOK {
			t.Fatalf("reading fixture daemon identity: HTTP %d, %v", status, err)
		}
		var info struct{ ID string }
		if err := json.Unmarshal(body, &info); err != nil {
			t.Fatalf("decoding fixture daemon identity: %v", err)
		}
		if info.ID == "" {
			t.Fatal("fixture Docker info returned no daemon identity")
		}
		return endpoint, info.ID
	}
	firstEndpoint, firstDaemon := read(first)
	secondEndpoint, secondDaemon := read(second)
	if firstEndpoint != secondEndpoint {
		t.Fatalf("fresh independent servers did not expose colliding endpoint IDs: %d and %d", firstEndpoint, secondEndpoint)
	}
	if firstDaemon != secondDaemon {
		t.Fatal("servers attached to the same Docker socket reported different daemon identities")
	}
	for _, cfg := range []*portainer.Target{first, second} {
		endpoint, daemon := read(cfg)
		if endpoint != firstEndpoint || daemon != firstDaemon {
			t.Fatal("repeated observation changed endpoint or daemon identity")
		}
	}
	wrong := *second
	wrong.PortainerKey = firstKey
	status, _, err := portainer.JSON(ctx, &wrong, "GET", "/api/endpoints", nil)
	if err != nil {
		t.Fatalf("checking wrong-server authentication: %v", err)
	}
	if status != http.StatusUnauthorized && status != http.StatusForbidden {
		t.Fatalf("wrong-server key must be refused, got HTTP %d", status)
	}
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
	for _, separate := range []bool{false, true} {
		t.Run(fmt.Sprintf("separate-connections-%t", separate), func(t *testing.T) { portainerIndependentRecovery(t, separate) })
	}
}

func portainerIndependentRecovery(t *testing.T, separate bool) {
	ctx := context.Background()
	slug := fmt.Sprintf("pt%d-%t", os.Getpid(), separate)
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
	secrets := secret.NewService(db, secret.Config{DataDir: dir})
	projects := project.New(db, nil)
	integrations := integration.NewService(db, integration.Dependencies{Projects: projects, Secrets: secrets})
	authority := &grants{who: "alice", deny: map[string]bool{}}
	release := &portainerRecoveryRelease{slug: slug, secrets: map[string]string{}}
	mustExec(t, db, `CREATE TABLE test_release_queue(release_id UUID PRIMARY KEY REFERENCES releases(id))`)
	s := assembled(db, testRuntime{Scope: docker.Scope{Client: docker.CLI{Binary: "docker"}}, DataDir: dir, Needs: catalog, Secrets: secrets, Projects: projects, Integrations: integrations, ChownImage: stock.Images().Sh, BuiltInTarget: "local"},
		deployment.Dependencies{Authority: authority, Configuration: deployment.ConfigurationDependencies{Authority: authority, Projects: release}, Releases: deployment.ReleaseDependencies{Authority: authority, Still: authority, Sources: release, Secrets: release, Queue: requestQueue{}}})
	target, err := s.AttachPortainerTarget(ctx, deployment.PortainerAttachment{Name: "customer-a", Environment: "lan", URLFrom: "PORTAINER_URL", APIKeyFrom: "PORTAINER_KEY", Domain: "lan"})
	if err != nil {
		t.Fatal(err)
	}
	dep, err := s.Deployments.EnsureStanding(ctx, pid, "customer-a", target.ID, deployment.Attributes{}.WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Targets.Offer(ctx, target, "*", deployment.OfferCaps{}, "alice"); err != nil {
		t.Fatal(err)
	}
	address, err := s.EditAddress(ctx, pid, dep.ID, 0, slug+".customer.example")
	if err != nil {
		t.Fatal(err)
	}
	laterDomain := "changed.example"
	if _, err := s.Targets.EditSettings(ctx, target.Name, deployment.TargetSettingsEdit{ExpectedRevision: &target.SettingsRevision, Domain: &laterDomain}, "alice"); err != nil {
		t.Fatal(err)
	}
	// The stack and its volumes are named by the deployment, as the
	// Portainer release names them.
	prefix, base := portainer.StackNames(dep.ID)
	stack := portainer.StackName(prefix, base)
	cleanupPortainerStack(t, stack)
	// The app's data folder was there before the first deploy (an earlier
	// take-down kept it): compose reuses it, and it is not the deployment's.
	if out, err := dockertest.CombinedOutput("volume", "create", "--label", "com.docker.compose.project="+stack, "--label", "com.docker.compose.volume=apptown-data-"+base, stack+"_apptown-data-"+base); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	// The release by name: the bundle's images (public ones here), built for
	// the environment's platform, rendered and run by the Portainer driver.
	contract := `app:
  web:
    public: true
    image: ` + stock.Images().DF + `
    command: ["sh", "-c", "mkdir -p /tmp/site; echo ready > /tmp/site/index.html; exec httpd -f -p 8080 -h /tmp/site"]
    ports: ["8080"]
    dataDir: /data
  worker:
    image: ` + stock.Images().DF + `
    command: ["sh", "-c", "trap 'exit 0' TERM; while true; do sleep 1; done"]
init:
  - name: migrate
    image: ` + stock.Images().DF + `
    command: ["true"]
needs:
  postgres: {}
  s3: {}
`
	cfg := &portainer.Target{PortainerURL: url, PortainerKey: key, EnvName: "lan"}
	endpoint, err := portainer.EndpointID(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	platform, err := portainer.EndpointPlatform(ctx, cfg, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	release.prepare(t, s, pid, contract, platform)
	release.apply(t, s, pid, dep.Name, "first-secret")
	verifyIngress := func() {
		t.Helper()
		verified, err := s.providers.PortainerConfig(ctx, target)
		if err != nil {
			t.Fatal(err)
		}
		stacks, err := portainer.ListStacks(ctx, verified)
		if err != nil {
			t.Fatal(err)
		}
		for _, running := range stacks {
			if running.Name != stack {
				continue
			}
			observed, err := portainer.ReadStackIngress(ctx, *verified, running, dep.ID, nil)
			if err != nil || !observed.Same(address.Ingress) {
				t.Fatalf("provider changed admitted host/certificate rules: %+v %v", observed, err)
			}
			return
		}
		t.Fatal("recorded provider stack is absent")
	}
	verifyIngress()
	siblingTarget := target
	if separate {
		otherURL, otherKey := localPortainer(t, "forge-it-portainer-other-"+slug)
		for name, value := range map[string]string{"OTHER_PORTAINER_URL": otherURL, "OTHER_PORTAINER_KEY": otherKey} {
			if err := secrets.SetBootstrap(ctx, name, value, "fixture"); err != nil {
				t.Fatal(err)
			}
		}
		siblingTarget, err = s.AttachPortainerTarget(ctx, deployment.PortainerAttachment{Name: "second-connection", Environment: "lan", URLFrom: "OTHER_PORTAINER_URL", APIKeyFrom: "OTHER_PORTAINER_KEY", Domain: "other.example"})
		if err != nil {
			t.Fatal(err)
		}
		if siblingTarget.ConnectionID == target.ConnectionID {
			t.Fatal("second connection reused identity")
		}
		if err := s.Targets.Offer(ctx, siblingTarget, "*", deployment.OfferCaps{}, "alice"); err != nil {
			t.Fatal(err)
		}
	}
	verifySibling := portainerSiblingRecovery(t, s, pid, siblingTarget, release)
	verifySibling(false)
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

	b, err := s.TakeBackup(ctx, pid, "customer-a", "manual", "bls")
	if err != nil || b.Status != BackupOK || len(b.Items) != 3 {
		t.Fatalf("the set: %+v %v", b, err)
	}
	// The data changes, and the set is put back.
	// sql-guarantee: the app's own Postgres in the stack, not AppTown's records
	psql("delete from notes")
	s3("-X", "PUT", "-H", "Content-Type: text/plain", "--data-binary", "changed", "http://s3:9000/app/logo.png")
	_ = dockertest.Run("exec", app, "rm", "/data/notes.txt")
	res, err := s.restoreBackup(ctx, nil, pid, "customer-a", b.ID, "bls")
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

	verifyIngress() // application-data restoration leaves routing and certificate input intact.
	changed, err := s.EditAddress(ctx, pid, dep.ID, address.Revision, "changed-"+slug+".customer.example")
	if err != nil {
		t.Fatal(err)
	}
	if changed.Ingress.URL == address.Ingress.URL {
		t.Fatal("address update did not change the release destination")
	}
	release.apply(t, s, pid, dep.Name, "first-secret-updated")
	verifySibling(false)
	removed, err := s.TakeDown(ctx, RemoveDeployment{ProjectID: pid, Name: "customer-a"})
	if err != nil || removed.Removed == nil || removed.Removed.FinalBackup == nil {
		t.Fatalf("take-down: %+v %v", removed, err)
	}
	for v, want := range map[string]bool{stack + "_apptown-need-" + base + "-postgres": false, stack + "_apptown-need-" + base + "-s3": false, stack + "_apptown-data-" + base: true} {
		if there, _ := (docker.Scope{Client: docker.CLI{Binary: "docker"}}).Exists(ctx, "volume", "inspect", v); there != want {
			t.Fatalf("after the take-down the volume %s is there %v, want %v", v, there, want)
		}
	}
	verifySibling(true) // first removal cannot delete or restore the second placement
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

// The label is the newly allocated deployment UUID stack, never an instance or
// user-provided prefix. Check cleanup rather than leaving silent test resources.
func cleanupPortainerStack(t *testing.T, stack string) {
	t.Helper()
	t.Cleanup(func() {
		for _, kind := range []string{"container", "network", "volume"} {
			list := []string{kind, "ls", "-q", "--filter", "label=com.docker.compose.project=" + stack}
			if kind == "container" {
				list = []string{"ps", "-aq", "--filter", "label=com.docker.compose.project=" + stack}
			}
			raw, err := dockertest.Output(list...)
			if err != nil {
				t.Errorf("listing owned %s cleanup: %v", kind, err)
				continue
			}
			owned := strings.Fields(string(raw))
			if len(owned) > 0 {
				command := []string{kind, "rm"}
				if kind == "container" || kind == "volume" {
					command = append(command, "-f")
				}
				if out, err := dockertest.CombinedOutput(append(command, owned...)...); err != nil {
					t.Errorf("cleaning owned %s: %v %s", kind, err, out)
				}
			}
			remaining, err := dockertest.Output(list...)
			if err != nil || len(strings.Fields(string(remaining))) > 0 {
				t.Errorf("owned %s survived cleanup: %s %v", kind, remaining, err)
			}
		}
	})
}

// Return a readback that proves the second placement is unchanged by the first's
// update/removal. The final call also restores its own backup after changing data.
func portainerSiblingRecovery(t *testing.T, s *both, pid ids.ID, target *deployment.Target, release *portainerRecoveryRelease) func(bool) {
	t.Helper()
	ctx := t.Context()
	dep, err := s.Deployments.EnsureStanding(ctx, pid, "customer-b", target.ID, deployment.Attributes{}.WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	address, err := s.EditAddress(ctx, pid, dep.ID, 0, "second-"+dep.ID.String()+".customer.example")
	if err != nil {
		t.Fatal(err)
	}
	prefix, base := portainer.StackNames(dep.ID)
	stack := portainer.StackName(prefix, base)
	cleanupPortainerStack(t, stack)
	released := release.apply(t, s, pid, dep.Name, "second-secret")
	if released.URL != address.Ingress.URL {
		t.Fatalf("second release address: %s, want %s", released.URL, address.Ingress.URL)
	}
	app, pg := stack+"-app-1", stack+"-postgres-1"
	for _, container := range []string{app, pg, stack + "-s3-1"} {
		if !poll.Until(ctx, 120*time.Second, time.Second, func() bool {
			out, err := dockertest.Output("inspect", "-f", "{{.State.Running}}{{if .State.Health}} {{.State.Health.Status}}{{end}}", container)
			state := strings.TrimSpace(string(out))
			return err == nil && (state == "true" || state == "true healthy")
		}) {
			t.Fatalf("second container %s did not become ready", container)
		}
	}
	if !poll.Until(ctx, 120*time.Second, time.Second, func() bool {
		out, _ := dockertest.Output("exec", pg, "pg_isready", "-U", "app", "-d", "app")
		return strings.Contains(string(out), "accepting connections")
	}) {
		t.Fatal("second database did not become ready")
	}
	sql := func(query string) string {
		t.Helper()
		out, err := dockertest.CombinedOutput("exec", pg, "psql", "-U", "app", "-d", "app", "-tA", "-c", query)
		if err != nil {
			t.Fatalf("second app database: %v %s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	// sql-guarantee: independent disposable application data, not AppTown records.
	sql("create table notes (body text); insert into notes values ('second row')")
	if out, err := dockertest.CombinedOutput("exec", app, "sh", "-c", "echo second-file > /data/notes.txt"); err != nil {
		t.Fatalf("second file: %v %s", err, out)
	}
	backup, err := s.TakeBackup(ctx, pid, dep.Name, "manual", "alice")
	if err != nil || backup.Status != BackupOK {
		t.Fatalf("second backup: %+v %v", backup, err)
	}
	for _, item := range backup.Items {
		if item.Volume != "" && !strings.HasPrefix(item.Volume, stack+"_") {
			t.Fatalf("second backup contains foreign volume: %+v", item)
		}
	}
	// Current data deliberately differs from the backup: an unintended restore
	// of this placement is also damage, not evidence that removal preserved it.
	// sql-guarantee: disposable application data distinguishes current state from backup.
	sql("update notes set body = 'second current row'")
	if out, err := dockertest.CombinedOutput("exec", app, "sh", "-c", "echo second-current-file > /data/notes.txt"); err != nil {
		t.Fatalf("second current file: %v %s", err, out)
	}
	assertData := func(wantRow, wantFile string) {
		t.Helper()
		if got := sql("select body from notes"); got != wantRow {
			t.Fatalf("second data changed before intended restore: %q want %q", got, wantRow)
		}
		if out, err := dockertest.Output("exec", app, "cat", "/data/notes.txt"); err != nil || strings.TrimSpace(string(out)) != wantFile {
			t.Fatalf("second file changed: %s %v want %s", out, err, wantFile)
		}
	}
	password, err := s.Service.env.NeedCredentials(deployment.NeedSecretsScope(dep.ID))(ctx, "postgres", "password")
	if err != nil || password == "" {
		t.Fatal("second database credentials missing", err)
	}
	return func(restore bool) {
		t.Helper()
		cfg, err := s.providers.PortainerConfig(ctx, target)
		if err != nil {
			t.Fatal(err)
		}
		stacks, err := portainer.ListStacks(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, st := range stacks {
			if st.Name == stack {
				found = true
				observed, err := portainer.ReadStackIngress(ctx, *cfg, st, dep.ID, nil)
				if err != nil || !observed.Same(address.Ingress) {
					t.Fatalf("second routing changed: %+v %v", observed, err)
				}
			}
		}
		if !found {
			t.Fatal("second owned stack disappeared")
		}
		if out, err := dockertest.Output("inspect", "-f", "{{.State.Running}}", app); err != nil || strings.TrimSpace(string(out)) != "true" {
			t.Fatalf("second app stopped: %s %v", out, err)
		}
		if out, err := dockertest.Output("exec", app, "sh", "-c", "printf '%s' \"$DEPLOYMENT_SECRET\""); err != nil || string(out) != "second-secret" {
			t.Fatalf("second delivered secret changed: %v", err)
		}
		if out, err := dockertest.Output("exec", app, "sh", "-c", "printf '%s' \"$APPTOWN_ORIGINS\""); err != nil || !strings.Contains(string(out), address.Ingress.URL) || strings.Contains(string(out), "changed-") {
			t.Fatalf("second origin changed: %s %v", out, err)
		}
		got, err := s.Service.env.NeedCredentials(deployment.NeedSecretsScope(dep.ID))(ctx, "postgres", "password")
		if err != nil || got != password {
			t.Fatal("second database credential changed", err)
		}
		assertData("second current row", "second-current-file")
		if restore {
			// sql-guarantee: disposable second application's data proves backup ownership.
			sql("delete from notes")
			restored, err := s.restoreBackup(ctx, nil, pid, dep.Name, backup.ID, "alice")
			if err != nil || restored.Status != RestoreNotVerified {
				t.Fatalf("second own backup restore: %+v %v", restored, err)
			}
		}
		if restore {
			assertData("second row", "second-file")
		}
	}
}
