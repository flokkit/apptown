package deployment

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/deploy/docker/dockerfake"
	"flokkit.io/forge/internal/forge/dependency"
	"flokkit.io/forge/internal/forge/integration"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/secret"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
	"flokkit.io/forge/stock"
)

// hostWorld is Deployment on this forge's own Docker, a fake daemon whose
// app answers a page, with project "p", the stock dependency catalog and
// the built-in target "local".
func hostWorld(t *testing.T, publicHost string) (*Service, *database.DB, *dockerfake.Daemon, string, ids.ID) {
	t.Helper()
	db := testDB(t)
	projectID := createTestProject(t, db, "p", "P")
	dir := t.TempDir()
	projects := project.New(db, nil)
	secrets := secret.NewService(db, secret.Config{DataDir: dir})
	integrations := integration.NewService(db, integration.Dependencies{Projects: projects, Secrets: secrets})
	catalog := dependency.NewCatalog(db, nil)
	if _, _, err := catalog.SyncStock(context.Background(), stock.FS, "dependencies"); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html><body><script>app()</script></body></html>"))
	}))
	t.Cleanup(server.Close)
	_, portText, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portText)
	client := dockerfake.New()
	client.SetPort(port)
	s := New(context.Background(), db, assembled(db, testRuntime{Scope: docker.Scope{Client: client}, Probe: &docker.Probe{Settle: time.Millisecond}, DataDir: dir, Projects: projects, Secrets: secrets, Integrations: integrations, Needs: catalog, BuiltInTarget: "local", Routes: Routing{PublicHost: publicHost}}, Dependencies{}))
	if _, err := s.Targets.Put(context.Background(), &Target{Name: "local", Kind: KindDockerHost, Origin: OriginBuiltIn, Status: TargetReady}); err != nil {
		t.Fatal(err)
	}
	return s, db, client, dir, projectID
}

// writeBootFixture writes an app declaring resolvedStackFixture into dir.
func writeBootFixture(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".apptown"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".apptown", "app.yaml"), []byte(resolvedStackFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A standing deployment on this forge's own host with a published port and
// no tier domain is reached on the forge host only when people reach the
// forge from elsewhere (a public host); a deployment not standing here is
// where it was said to be, with no mark the owner cannot vouch for.
func TestAPortAddressWithAPublicHostIsMarkedForTheForgeHostOnly(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		publicHost, mainDomain string
		dev, main              bool
	}{
		{publicHost: "forge.example", dev: true, main: true},
		{publicHost: "forge.example", mainDomain: "apps.example", dev: true, main: false},
		{publicHost: "", dev: false, main: false},
	} {
		s, _, _, _, projectID := hostWorld(t, c.publicHost)
		s.env.routes.MainDomain, s.env.routes.MainScheme = c.mainDomain, "https"
		local, err := s.Targets.ByName(t.Context(), "local")
		if err != nil {
			t.Fatal(err)
		}
		for name, want := range map[string]bool{"dev": c.dev, "main": c.main} {
			d := &Deployment{ProjectID: projectID, Name: name, Kind: Standing, TargetID: local.ID, HostPort: 40001}
			if got := s.Reach(t.Context(), "p", d); got.HostOnly != want {
				t.Errorf("public host %q, main domain %q: %s reached at %+v, want host-only %v", c.publicHost, c.mainDomain, name, got, want)
			}
		}
	}
	s, _, _, _, projectID := hostWorld(t, "forge.example")
	if got := s.Reach(t.Context(), "p", &Deployment{ProjectID: projectID, Name: "x", URL: "https://elsewhere.example"}); got.HostOnly || got.URL != "https://elsewhere.example" {
		t.Fatalf("recorded address: %+v", got)
	}
}

// On an attached host with an ingress domain, main is shown at the
// project's name and any other deployment at the host and its port, as its
// container is routed (docker.Scope.RoutesDeployment), whatever address was
// recorded before: a second deployment shown at main's name was given
// main's route too (soak finding 55).
func TestAnAttachedHostShowsTheProjectsNameForMainOnly(t *testing.T) {
	t.Parallel()
	s, _, _, _, projectID := hostWorld(t, "")
	attached, err := s.Targets.Put(t.Context(), &Target{Name: "droplet", Kind: KindDockerHost, Origin: OriginAttached, Status: TargetReady,
		Address: map[string]any{"host": "ssh://deploy@droplet.example", "keyFrom": "DROPLET_KEY"}, Ingress: map[string]any{"domain": "apps.example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"main": "https://p.apps.example.com", "fresh": "http://droplet.example:40002"} {
		d := &Deployment{ProjectID: projectID, Name: name, Kind: Standing, TargetID: attached.ID, HostPort: 40002, URL: "https://p.apps.example.com"}
		if got := s.Reach(t.Context(), "p", d); got.URL != want || got.HostOnly {
			t.Errorf("%s reached at %+v, want %s", name, got, want)
		}
	}
}

// Boot reconcile forgets a preview its host says is gone, never one whose
// host could not be asked because reconcile itself was stopped: that is
// unknown, and its row stays.
func TestBootReconcileCutShortForgetsNoPreview(t *testing.T) {
	t.Parallel()
	s, _, _, _, projectID := hostWorld(t, "")
	if err := s.Deployments.RecordPreview(context.Background(), projectID, idstest.N(9), idstest.N(1), docker.PreviewChangeName(idstest.N(9)), 4000, "http://p"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	present := func(ids.ID, string) (ContainerPresence, error) {
		cancel() // the daemon stops while the host is being asked
		return ContainerGone, ctx.Err()
	}
	s.BootReconcile(ctx, present, func(ProjectDeployment) error { return nil })
	if _, err := s.Deployments.ByName(context.Background(), projectID, PreviewName(idstest.N(9))); err != nil {
		t.Fatalf("a preview whose host could not be asked was forgotten: %v", err)
	}
}
