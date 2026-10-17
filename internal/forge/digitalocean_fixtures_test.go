package forge

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/deploy/digitalocean"
	"flokkit.io/forge/internal/deploy/digitalocean/dofake"
	"flokkit.io/forge/internal/forge/deployment"
	"flokkit.io/forge/internal/forge/project"
)

// digitalOceanWorld adds Forge's non-HTTP effect observations to the shared API.
// Each fixture owns its driver options and fake API; callers can run in parallel.
type digitalOceanWorld struct {
	*dofake.API
	owned, probed *[]string
}

// doWorld: a project with a repository whose main carries the postgres
// contract, a bundle cut from it, a DigitalOcean connector whose token
// resolves, the target it offers, and the fake API behind it all.
func doWorld(t *testing.T) (*App, *digitalOceanWorld, *project.Project, *deployment.ReleaseBundle) {
	t.Helper()
	f := &digitalOceanWorld{API: &dofake.API{}}
	srv := httptest.NewServer(f.Handler())
	t.Cleanup(srv.Close)
	options := digitalocean.Options{APIBase: srv.URL, PollInterval: 10 * time.Millisecond, DBPoll: 10 * time.Millisecond}
	var probed []string
	options.ProbeFrontDoor = func(ctx context.Context, url string, _ time.Time, logf func(string, ...any)) error {
		probed = append(probed, url)
		return nil
	}
	f.probed = &probed
	var owned []string
	options.OwnDatabase = func(ctx context.Context, adminURI, dbName, dbUser string) error {
		owned = append(owned, adminURI+" "+dbName+"<-"+dbUser)
		return nil
	}
	f.owned = &owned
	options.PushImage = func(d *digitalocean.Driver, ctx context.Context, c *digitalocean.Client, registry, repo, local, tag string, logf func(string, ...any)) (string, error) {
		logf("pushed %s as %s/%s:%s (fake)", local, registry, repo, tag)
		return "sha256:" + strings.Repeat("a", 64), nil
	}
	a := testApp(t, Config{DockerClient: newFakeDocker(), DigitalOcean: options})
	a.SyncStockDependencies(context.Background())

	if err := os.WriteFile(filepath.Join(a.cfg.dataDir, "secrets.env"), []byte("DIGITALOCEAN_ACCESS_TOKEN=dop_v1_test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pid, sha, _ := fleetRepo(t, a, "expense", map[string]string{
		".forge/app.yaml": "app:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\n    health:\n      readiness: { http: /healthz }\ninit:\n  - name: migrate\n    build: .\n    command: [\"dotnet\", \"App.dll\", \"--init\"]\nneeds:\n  postgres:\n    version: \"16\"\n",
		"Dockerfile":      "FROM scratch\n",
	})
	if _, err := a.Integrations.Bindings.Save(context.Background(), "do-syd", digitalocean.CapCloudAccount, "digitalocean", mustJSON(map[string]any{"tokenFrom": "DIGITALOCEAN_ACCESS_TOKEN", "region": "syd", "registry": "forge-test"}), "fixture"); err != nil {
		t.Fatal(err)
	}
	a.SeedTargets(t.Context())
	contractBytes, err := os.ReadFile(filepath.Join(a.projectRepoPath("expense"), ".forge", "app.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	bid, err := a.Deployment.Bundles.Cut(t.Context(), pid, sha, map[string]string{"web": "sha256:web"}, map[string]string{"migrate": "sha256:web"}, contractBytes, deployment.BundleOrigin{})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := a.Deployment.Bundles.ByID(context.Background(), bid)
	p, _ := a.Projects.BySlug(context.Background(), "expense")
	return a, f, &p, b
}
