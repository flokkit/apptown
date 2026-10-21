package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/deploy/digitalocean"
	"flokkit.io/forge/internal/deploy/digitalocean/dofake"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/deploy/docker/dockerfake"
	"flokkit.io/forge/internal/forge/dependency"
	"flokkit.io/forge/internal/forge/deployment"
	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/forge/integration"
	"flokkit.io/forge/internal/forge/jobs"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/secret"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/stock"
)

const targetTestBuiltin = "built-in-docker"

type targetTestConnectors struct{}

type targetTestDeclarations struct{}

func (targetTestDeclarations) Declared(string) (deployment.Declared, bool) {
	return deployment.Declared{}, false
}

func (targetTestConnectors) RetireUnusedConnector(ctx context.Context, tx *database.Tx, provider, target, who string, at time.Time) error {
	return integration.RetireConnectorIn(ctx, tx, provider, digitalocean.CapCloudAccount, target, who, at)
}

type targetHTTPWorld struct {
	db           *database.DB
	dir          string
	projects     *project.Projects
	identity     *identity.Service
	integrations *integration.Service
	needs        *dependency.Catalog
	secrets      *secret.Service
	deployment   *deployment.Service
	hosts        *deployment.Hosts
	providers    *deployment.Providers
	cloud        digitalocean.Options
	docker       *dockerfake.Daemon
	targets      Targets
	infra        Infrastructure
}

func newTargetHTTPWorld(t *testing.T, options ...digitalocean.Options) *targetHTTPWorld {
	t.Helper()
	db := databaseSuite.New(t)
	dir := t.TempDir()
	projects := project.New(db, nil)
	secrets := secret.NewService(db, secret.Config{DataDir: dir})
	integrations := integration.NewService(db, integration.Dependencies{Projects: projects, Secrets: secrets})
	needs := dependency.NewCatalog(db, nil)
	if _, _, err := needs.SyncStock(context.Background(), stock.FS, "dependencies"); err != nil {
		t.Fatal(err)
	}
	identity := identity.New(db, identity.Config{Projects: project.NewCatalogue(db)})
	daemon := dockerfake.New()
	var cloud digitalocean.Options
	if len(options) > 0 {
		cloud = options[0]
	}
	w := &targetHTTPWorld{db: db, dir: dir, projects: projects, identity: identity, integrations: integrations, needs: needs, secrets: secrets, cloud: cloud, docker: daemon}
	w.setDockerClient(daemon)
	return w
}

func (w *targetHTTPWorld) seedBuiltin(t *testing.T) *deployment.Target {
	t.Helper()
	bi, err := w.deployment.Targets.Put(context.Background(), &deployment.Target{Name: targetTestBuiltin, Kind: deployment.KindDockerHost, Origin: deployment.OriginBuiltIn, Address: map[string]any{"daemon": "local"}, Capabilities: deployment.TargetCapabilities{PersistentVolumes: true, Previews: true, Needs: w.needs.EmulatorKinds(context.Background())}, NeedsMode: "emulators", SecretsDelivery: "inject", Status: deployment.TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	return bi
}

func (w *targetHTTPWorld) createProject(t *testing.T, slug string) ids.ID {
	t.Helper()
	id, err := database.Transact(context.Background(), w.db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: slug, Name: strings.ToUpper(slug)})
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// setDockerClient builds the world's deployment owner and its handlers
// over client as this AppTown's Docker.
func (w *targetHTTPWorld) setDockerClient(client docker.Client) {
	targets, deployments := deployment.NewTargets(w.db, targetTestConnectors{}), deployment.NewDeployments(w.db)
	w.hosts = deployment.NewHosts(targets, w.secrets, deployment.HostSettings{Scope: docker.Scope{Client: client}, DataDir: w.dir, SSHConfigPath: filepath.Join(w.dir, "ssh-config"), BuiltInTarget: targetTestBuiltin})
	environment := deployment.NewEnvironment(targets, deployment.EnvironmentDependencies{Secrets: w.secrets, Integrations: w.integrations, Needs: w.needs, Projects: w.projects})
	w.providers = deployment.NewProviders(context.Background(), w.db, deployment.ProviderRecords{Targets: targets, Deployments: deployments}, w.hosts, environment,
		deployment.ProviderDependencies{Projects: w.projects, Secrets: w.secrets, Integrations: w.integrations, DigitalOcean: w.cloud})
	w.deployment = deployment.New(context.Background(), w.db, deployment.Dependencies{Targets: targets, Deployments: deployments, Hosts: w.hosts, Environment: environment, Providers: w.providers,
		Projects: w.projects, Declared: targetTestDeclarations{}})
	w.targets = NewTargets(w.deployment, w.hosts, w.providers, w.projects, w.identity, w.integrations, w.needs, w.db, nil)
	w.infra = NewInfrastructure(w.deployment, w.hosts, w.providers, w.projects, w.db, &targetTestInventoryCache{}, nil, false, "", "", deployment.IntranetTargetName, nil)
}

type targetTestInventoryCache struct{}

type targetDockerClientFunc func(context.Context, string, docker.Command) ([]byte, error)

func (f targetDockerClientFunc) Run(ctx context.Context, host string, req docker.Command) ([]byte, error) {
	return f(ctx, host, req)
}

func (*targetTestInventoryCache) Live(context.Context, *jobs.Queue, string, deploy.Inventorier, bool) ([]deploy.Resource, string, time.Time, bool) {
	return nil, "", time.Time{}, false
}
func (*targetTestInventoryCache) Invalidate() {}

func targetCall(t *testing.T, handler http.HandlerFunc, method, path, body string, values map[string]string) (int, map[string]any) {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r = r.WithContext(access.WithCaller(r.Context(), access.Caller{Principal: access.Principal{Kind: "user", Name: "bls", Via: "test"}, Verified: true, Receipt: "bls"}))
	for k, v := range values {
		r.SetPathValue(k, v)
	}
	rec := httptest.NewRecorder()
	handler(rec, r)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func writeTargetSecret(t *testing.T, w *targetHTTPWorld, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(w.dir, "secrets.env"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func targetDOWorld(t *testing.T) (*targetHTTPWorld, *dofake.API) {
	t.Helper()
	api := &dofake.API{}
	server := httptest.NewServer(api.Handler())
	t.Cleanup(server.Close)
	w := newTargetHTTPWorld(t, digitalocean.Options{APIBase: server.URL})
	writeTargetSecret(t, w, "DIGITALOCEAN_ACCESS_TOKEN=dop_v1_test\n")
	return w, api
}
