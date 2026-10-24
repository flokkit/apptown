package preview

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/dbschema"
	"github.com/flokkit/apptown/internal/apptown/dependency"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/docker/dockerfake"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/stock"
)

// The package's rules are tested against the canonical schema, each test
// on its own clone of one template database (internal/dbtest).
var databaseSuite = dbtest.NewSuite(dbschema.Open)

func TestMain(m *testing.M) {
	code := databaseSuite.Run(m)
	gittest.RemoveTemplates()
	os.Exit(code)
}

func testDB(t *testing.T) *database.DB { t.Helper(); return databaseSuite.New(t) }

func createTestProject(t *testing.T, db *database.DB, slug, name string) ids.ID {
	t.Helper()
	return createProjectFor(t, db, slug, name, project.Ordinary)
}

func createProjectFor(t *testing.T, db *database.DB, slug, name string, purpose project.Purpose) ids.ID {
	t.Helper()
	id, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: slug, Name: name, Purpose: purpose})
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

const resolvedStackFixture = `app:
  web:
    image: reg.example/app:1
    ports: ["8080"]
    public: true
    dataDir: /data
  worker:
    image: reg.example/worker:1
    command: ["node", "worker.js"]
init:
  - name: migrate
    image: reg.example/app:1
    command: ["./migrate.sh"]
needs:
  postgres: {}
`

// world is what a preview is built over in a test: Deployment's records and
// collaborators built from these settings once, the way the composition root
// builds them, and the owners they read.
type world struct {
	db        *database.DB
	dir       string
	projectID ids.ID
	client    *dockerfake.Daemon
	projects  *project.Projects
	secrets   *secret.Service
	catalog   *dependency.Catalog
	records   *deployment.Service // Deployment's records, seeded through its operations
	host      deployment.HostSettings
	env       deployment.EnvironmentDependencies
}

// newWorld is a project "p" on this AppTown's own Docker (a fake daemon whose
// app answers a page) with the stock dependency catalog and the built-in
// target "local"; secretsEnv, when set, is AppTown's secrets file.
func newWorld(t *testing.T, publicHost, secretsEnv string) *world {
	t.Helper()
	db := testDB(t)
	w := &world{db: db, dir: t.TempDir(), projectID: createTestProject(t, db, "p", "P"), projects: project.New(db, nil)}
	if secretsEnv != "" {
		if err := os.WriteFile(filepath.Join(w.dir, "secrets.env"), []byte(secretsEnv), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	w.secrets = secret.NewService(db, secret.Config{DataDir: w.dir})
	w.catalog = dependency.NewCatalog(db, nil)
	if _, _, err := w.catalog.SyncStock(context.Background(), stock.FS, "dependencies"); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		_, _ = rw.Write([]byte("<html><body><script>app()</script></body></html>"))
	}))
	t.Cleanup(server.Close)
	_, portText, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portText)
	w.client = dockerfake.New()
	w.client.SetPort(port)
	w.host = deployment.HostSettings{Scope: docker.Scope{Client: w.client}, DataDir: w.dir, BuiltInTarget: "local"}
	w.env = deployment.EnvironmentDependencies{ToolVersions: stock.Versions(), Secrets: w.secrets, Integrations: integration.NewService(db, integration.Dependencies{Projects: w.projects, Secrets: w.secrets}),
		Needs: w.catalog, Probe: &docker.Probe{Settle: time.Millisecond}, Routes: deployment.Routing{PublicHost: publicHost}, Projects: w.projects}
	targets := deployment.NewTargets(db, nil)
	w.records = deployment.New(context.Background(), db, deployment.Dependencies{Targets: targets, Projects: w.projects,
		Hosts: deployment.NewHosts(targets, w.secrets, w.host), Environment: deployment.NewEnvironment(targets, w.env)})
	if _, err := w.records.Targets.Put(context.Background(), &deployment.Target{Name: "local", Kind: deployment.KindDockerHost, Origin: deployment.OriginBuiltIn, Status: deployment.TargetReady}); err != nil {
		t.Fatal(err)
	}
	return w
}

// service is the preview owner over the world, with deps' ports.
func (w *world) service(deps Dependencies) *Service {
	deps.Hosts = deployment.NewHosts(w.records.Targets, w.secrets, w.host)
	deps.Environment = deployment.NewEnvironment(w.records.Targets, w.env)
	deps.Targets, deps.Deployments, deps.Releases = w.records.Targets, w.records.Deployments, w.records.Releases
	deps.Bundles, deps.Retained = w.records.Bundles, w.records.Retained
	deps.Projects, deps.Integrations, deps.DataDir = w.projects, w.env.Integrations, w.dir
	return New(w.db, deps)
}

// reconfigure gives s the world's collaborators built again with changed
// settings: what an AppTown started with other settings would have.
func (w *world) reconfigure(s *Service, host func(*deployment.HostSettings), env func(*deployment.EnvironmentDependencies)) {
	if host != nil {
		host(&w.host)
	}
	if env != nil {
		env(&w.env)
	}
	s.hosts = deployment.NewHosts(w.records.Targets, w.secrets, w.host)
	s.env = deployment.NewEnvironment(w.records.Targets, w.env)
}

// previewBootWorld is the preview owner on the world's Docker.
func previewBootWorld(t *testing.T, publicHost string) (*Service, *database.DB, *dockerfake.Daemon, string, ids.ID) {
	t.Helper()
	w := newWorld(t, publicHost, "")
	return w.service(Dependencies{}), w.db, w.client, w.dir, w.projectID
}

func writePreviewBootFixture(t *testing.T, dir string) {
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

func previewCommands(client *dockerfake.Daemon) []string {
	var calls []string
	for _, args := range client.Commands() {
		calls = append(calls, strings.Join(args, " "))
	}
	return calls
}
