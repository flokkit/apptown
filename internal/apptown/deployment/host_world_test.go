package deployment

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/dbtest"

	"github.com/flokkit/apptown/internal/apptown/dependency"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/docker/dockerfake"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
	"github.com/flokkit/apptown/stock"
)

// hostWorld is Deployment on this AppTown's own Docker, a fake daemon whose
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
	client := newDockerFake()
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

// A standing deployment on this AppTown's own host with a published port and
// no tier domain is reached on the AppTown host only when people reach the
// AppTown from elsewhere (a public host); a deployment not standing here is
// where it was said to be, with no mark the owner cannot vouch for.
func TestAPortAddressWithAPublicHostIsMarkedForAppTownHostOnly(t *testing.T) {
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
			if got := s.reach(t.Context(), "p", d); got.HostOnly != want {
				t.Errorf("public host %q, main domain %q: %s reached at %+v, want host-only %v", c.publicHost, c.mainDomain, name, got, want)
			}
		}
	}
	s, _, _, _, projectID := hostWorld(t, "forge.example")
	if got := s.reach(t.Context(), "p", &Deployment{ProjectID: projectID, Name: "x", URL: "https://elsewhere.example"}); got.HostOnly || got.URL != "https://elsewhere.example" {
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
		if got := s.reach(t.Context(), "p", d); got.URL != want || got.HostOnly {
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
	if err := s.Deployments.RecordPreview(context.Background(), projectID, idstest.N(9), idstest.N(1), docker.PreviewChangeName(idstest.N(9)), 4000, "http://p", nil); err != nil {
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

func TestDockerAddressesUseCapturedServingIdentityAndRefuseUnavailableIdentity(t *testing.T) {
	t.Parallel()
	s, db, daemon, _, pid := hostWorld(t, "forge.example")
	s.env.routes.MainScheme, s.env.routes.MainDomain = "https", "main.example"
	local, err := s.Targets.ByName(t.Context(), "local")
	if err != nil {
		t.Fatal(err)
	}
	attached, err := s.Targets.Put(t.Context(), &Target{Name: "serving-attached", Kind: KindDockerHost, Origin: OriginAttached, Status: TargetReady, Address: map[string]any{"host": "ssh://deploy@droplet.example", "keyFrom": "KEY"}, Ingress: map[string]any{"domain": "apps.example"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.InTx(t.Context(), func(tx *database.Tx) error {
		held, err := project.ByIDIn(t.Context(), tx, pid)
		if err != nil {
			return err
		}
		next := "renamed"
		_, err = project.UpdateSettingsIn(t.Context(), tx, pid, held.Slug, project.SettingsPatch{ExpectedRevision: &held.SettingsRevision, Slug: &next})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		target *Target
		want   string
	}{{local, "https://p.main.example"}, {attached, "https://p.apps.example"}} {
		dep := &Deployment{ProjectID: pid, Name: "main", Kind: Standing, TargetID: tc.target.ID, HostPort: 40001}
		if got := s.reach(t.Context(), "caller-display", dep); got.URL != tc.want {
			t.Fatalf("captured address=%+v want %s", got, tc.want)
		}
		clear := dbtest.NextStatementFailure(db, "-- name: ProjectByID")
		got := s.reach(t.Context(), "caller-display", dep)
		clear()
		if got.URL != "" {
			t.Fatalf("unavailable identity synthesized address: %+v", got)
		}
		daemon.ResetCommands()
		clear = dbtest.NextStatementFailure(db, "-- name: ProjectByID")
		_, err := s.providers.Driver(t.Context(), tc.target, dep)
		clear()
		if err == nil || len(daemon.Commands()) != 0 {
			t.Fatalf("unavailable identity reached driver effects: err=%v calls=%v", err, daemon.Commands())
		}
	}
}

func TestAPublicHostPortDoesNotRefuseABuiltInDeployment(t *testing.T) {
	t.Parallel()
	s, _, daemon, _, projectID := hostWorld(t, "localhost:7777")
	c, ws := llmContract(t, "app:\n  web:\n    image: reg.example/app:1\n    ports: [\"8080\"]\n    public: true\n")
	local, err := s.Targets.ByName(t.Context(), "local")
	if err != nil {
		t.Fatal(err)
	}
	dep, err := s.Deployments.EnsureStanding(t.Context(), projectID, "main", local.ID, Attributes{Feed: "main", Audience: "team", DataClass: "synthetic"})
	if err != nil {
		t.Fatal(err)
	}
	names, err := dep.HostNames()
	if err != nil {
		t.Fatal(err)
	}
	daemon.PublishPorts()
	t.Cleanup(daemon.Close)
	port, err := s.Deployments.stablePort(t.Context(), projectID, "main", names.App())
	if err != nil {
		t.Fatal(err)
	}
	source := deploy.Source{Workspace: ws, Revision: "revision", Slug: "p", Contract: c}
	body := []byte(`{"version":1,"revision":"revision"}`)
	art := deploy.Artifact{Kind: "docker-source", Body: body, Hash: deploy.ConfigHash(string(body)), Source: &source}
	run := idstest.N(7)
	out, err := s.RunLanding(t.Context(), landingClaim{projectID, run}, LandingInput{
		Start:  Start{DeploymentID: dep.ID, TargetID: dep.TargetID, Key: "public-port", Inputs: Inputs{ReleaseRun: run, Revision: "revision", ArtifactHash: art.Hash, DockerSource: string(art.Body)}},
		Source: *art.Source, HostPort: port, DataClass: "synthetic",
	})
	if err != nil || out.Boot.Err != nil || out.Execution.Outcome != Applied {
		t.Fatalf("landing: %+v %v", out, err)
	}
	stored, err := s.Deployments.ByID(t.Context(), dep.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("http://localhost:%d", port)
	if got := s.URL(t.Context(), "p", stored); got != want {
		t.Fatalf("address %q, want %q", got, want)
	}
	if !strings.Contains(fmt.Sprint(daemon.Commands()), "APPTOWN_APP_URL="+want) || !strings.Contains(fmt.Sprint(daemon.Commands()), "APPTOWN_ORIGINS="+want) {
		t.Fatalf("app did not receive its origin: %v", daemon.Commands())
	}
}
