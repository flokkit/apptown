package apptown

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/flokkit/apptown/internal/ids"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// --- the engine-side bridge (pre-dates application secrets) ---------------------

// infisicalFake stands in for Infisical for the application-secret tests:
// one project, environment prod, a fixed set of keys.
func infisicalFake(t *testing.T, a *App, keys map[string]string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/universal-auth/login":
			_ = json.NewEncoder(w).Encode(map[string]any{"accessToken": "tok", "expiresIn": 3600})
		case "/api/v3/secrets/raw":
			var list []map[string]string
			for k, v := range keys {
				list = append(list, map[string]string{"secretKey": k, "secretValue": v})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"secrets": list})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	env := "INFISICAL_URL=" + srv.URL + "\nINFISICAL_CLIENT_ID=cid\nINFISICAL_CLIENT_SECRET=csec\nINFISICAL_PROJECT_ID=proj1\n"
	if err := os.WriteFile(filepath.Join(a.cfg.dataDir, "secrets.env"), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSecretsBoundToInfisical(t *testing.T) {
	t.Parallel()
	a, f, ws := secretsWorld(t)
	infisicalFake(t, a, map[string]string{"QP_STRIPE": "sk_from_infisical", "QP_ROLE": "role_from_infisical"})
	c, err := docker.ReadWorkspaceContract(ws)
	if err != nil {
		t.Fatal(err)
	}
	for name, ref := range map[string]string{"STRIPE_API_KEY": "QP_STRIPE", "MIGRATION_DB_ROLE": "QP_ROLE"} {
		if err := a.Secrets.Bindings.Put(t.Context(), idstest.N(1), secret.Binding{Deployment: "main", Name: name, Store: secret.StoreInfisical, Ref: ref, Version: "infisical", UpdatedBy: "bls"}); err != nil {
			t.Fatal(err)
		}
	}
	res, err := testDockerHost(t, a, idstest.N(1)).Boot(context.Background(), docker.BootRequest{Docker: a.docker, Name: secretsNames.App(), StackID: secretsNames.Stack(), Workspace: ws,
		Deployment: "main", Persistent: true, Probe: 2 * time.Second}, c)
	if err != nil {
		t.Fatalf("boot: %v", err)
	}
	var app string
	for _, r := range containersRun(f) {
		if r.name == secretsNames.App() {
			app = r.call
		}
	}
	if !strings.Contains(app, "-e STRIPE_API_KEY=sk_from_infisical") {
		t.Fatalf("Infisical value must reach the web part: %s", app)
	}
	// Nothing from Infisical lands in AppTown's database.
	var n int
	// sql-guarantee: absence from storage in every scope is the guarantee; the owner reads one scope and name at a time
	_ = a.DB.QueryRow(`SELECT COUNT(*) FROM secret_values WHERE name IN ('STRIPE_API_KEY','MIGRATION_DB_ROLE')`).Scan(&n)
	if n != 0 {
		t.Fatal("an Infisical-bound value must never be stored by AppTown")
	}
	if rec := mustJSON(res.Stack.Secrets.Receipts); strings.Contains(rec, "sk_from") || !strings.Contains(rec, `"store":"infisical"`) {
		t.Fatalf("receipt: %s", rec)
	}
	// A binding to a key Infisical does not have is a clear refusal, not a silent blank.
	_ = a.Secrets.Bindings.Put(t.Context(), idstest.N(1), secret.Binding{Deployment: "main", Name: "STRIPE_API_KEY", Store: secret.StoreInfisical, Ref: "NOPE", Version: "infisical"})
	if _, err := testDockerHost(t, a, idstest.N(1)).Boot(context.Background(), docker.BootRequest{Docker: a.docker, Name: secretsNames.App(), StackID: secretsNames.Stack(), Workspace: ws,
		Deployment: "main", Persistent: true, Probe: 2 * time.Second}, c); err == nil || !strings.Contains(err.Error(), "NOPE") {
		t.Fatalf("missing Infisical key: %v", err)
	}
}

// testSpec is shared with the owning package's delivery tests.
var testSpec = func() string {
	raw, err := os.ReadFile("secret/testdata/declaration.toml")
	if err != nil {
		panic(err)
	}
	return string(raw)
}()

// secretsWorld: a project whose tree declares the test spec and a contract
// with a public web part, a worker, and a migrate init step.
func secretsWorld(t *testing.T) (*App, *fakeDocker, string) {
	t.Helper()
	a, f := bootWorld(t)
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, ".apptown"), 0o755); err != nil {
		t.Fatal(err)
	}
	contract := `version: 1
app:
  web:
    image: app:1
    ports: ["8080"]
    public: true
  worker:
    image: app:1
    command: ["worker"]
init:
  - name: migrate
    image: app:1
    command: ["migrate"]
needs:
  postgres: {}
secrets:
  generate:
    SECRET_KEY_BASE: deployment
`
	if err := os.WriteFile(filepath.Join(ws, ".apptown", "app.yaml"), []byte(contract), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, secret.SpecFile), []byte(testSpec), 0o644); err != nil {
		t.Fatal(err)
	}
	return a, f, ws
}

func TestSecretsDeliveredPerConsumer(t *testing.T) {
	t.Parallel()
	a, f, ws := secretsWorld(t)
	c, err := docker.ReadWorkspaceContract(ws)
	if err != nil {
		t.Fatal(err)
	}
	// Bind what the main deployment needs: STRIPE in the built-in store,
	// the migration role too. SECRET_KEY_BASE is generated on first boot.
	for _, name := range []string{"STRIPE_API_KEY", "MIGRATION_DB_ROLE"} {
		ver, err := a.Secrets.Vault.Put(t.Context(), secret.Scope("p", "main", ""), name, "value-of-"+name, "bls")
		if err != nil {
			t.Fatal(err)
		}
		if err := a.Secrets.Bindings.Put(t.Context(), idstest.N(1), secret.Binding{Deployment: "main", Name: name, Store: secret.StoreInternal,
			Ref: secret.Scope("p", "main", ""), Version: "1", UpdatedBy: "bls"}); err != nil || ver != 1 {
			t.Fatal(err)
		}
	}
	res, err := testDockerHost(t, a, idstest.N(1)).Boot(context.Background(), docker.BootRequest{Docker: a.docker, Name: secretsNames.App(), StackID: secretsNames.Stack(), Workspace: ws,
		Deployment: "main", HostPort: 0, Persistent: true, Probe: 2 * time.Second}, c)
	if err != nil {
		t.Fatalf("boot: %v\n%s", err, strings.Join(f.joined(), "\n"))
	}
	runs := containersRun(f)
	byName := map[string]string{}
	for _, r := range runs {
		byName[r.name] = r.call
	}
	app := byName[secretsNames.App()]
	init := byName[secretsNames.Init("migrate")]
	worker := byName[secretsNames.Service("worker")]
	if app == "" || init == "" || worker == "" {
		t.Fatalf("containers: %v", byName)
	}
	// web: its key and Stripe; not the migration role.
	if !strings.Contains(app, "-e STRIPE_API_KEY=value-of-STRIPE_API_KEY") || !strings.Contains(app, "-e SECRET_KEY_BASE=") || strings.Contains(app, "MIGRATION_DB_ROLE") {
		t.Fatalf("web env wrong: %s", app)
	}
	// worker: only the key.
	if !strings.Contains(worker, "-e SECRET_KEY_BASE=") || strings.Contains(worker, "STRIPE") || strings.Contains(worker, "MIGRATION") {
		t.Fatalf("worker env wrong: %s", worker)
	}
	// init: only the migration role.
	if !strings.Contains(init, "-e MIGRATION_DB_ROLE=value-of-MIGRATION_DB_ROLE") || strings.Contains(init, "SECRET_KEY_BASE") || strings.Contains(init, "STRIPE") {
		t.Fatalf("init env wrong: %s", init)
	}
	// Receipts name stores and versions, never values.
	rec := mustJSON(res.Stack.Secrets.Receipts)
	if strings.Contains(rec, "value-of-") || !strings.Contains(rec, `"name":"SECRET_KEY_BASE"`) || !strings.Contains(rec, `"generated":true`) {
		t.Fatalf("receipts: %s", rec)
	}
	if res.Stack.Secrets.Profile != "default" {
		t.Fatalf("profile: %s", res.Stack.Secrets.Profile)
	}
	// The generated key is stable across boots.
	first := boundSecrets(t, a, idstest.N(1), "main")["SECRET_KEY_BASE"]
	keyBefore := storedSecret(t, a, first.Ref, "SECRET_KEY_BASE")
	f.reset()
	if _, err := testDockerHost(t, a, idstest.N(1)).Boot(context.Background(), docker.BootRequest{Docker: a.docker, Name: secretsNames.App(), StackID: secretsNames.Stack(), Workspace: ws,
		Deployment: "main", Persistent: true, Probe: 2 * time.Second}, c); err != nil {
		t.Fatal(err)
	}
	keyAfter := storedSecret(t, a, first.Ref, "SECRET_KEY_BASE")
	if keyBefore == "" || keyBefore != keyAfter {
		t.Fatal("a generated secret must never be regenerated on deploy")
	}
	// A preview environment gets its OWN generated key (domain deployment)
	// and takes the preview profile, where Stripe is optional.
	f.reset()
	res, err = testDockerHost(t, a, idstest.N(1)).Boot(context.Background(), docker.BootRequest{Docker: a.docker, Name: "apptown-preview-change-00000000-0001-7000-8000-000000000000", StackID: docker.ChangeStackID(idstest.N(1)), Workspace: ws,
		Deployment: "preview", Probe: 2 * time.Second}, c)
	if err != nil {
		// The migration role is required in preview too and unbound there.
		if !isSecretsMissing(err) || !strings.Contains(err.Error(), "MIGRATION_DB_ROLE") || strings.Contains(err.Error(), "STRIPE") {
			t.Fatalf("preview should miss only the migration role: %v", err)
		}
	} else {
		t.Fatal("preview must refuse: MIGRATION_DB_ROLE is unbound there")
	}
	if len(containersRun(f)) != 0 {
		t.Fatal("a missing secret must stop the boot before any container exists")
	}
	if pk := boundSecrets(t, a, idstest.N(1), "preview")["SECRET_KEY_BASE"]; pk.Ref == first.Ref {
		t.Fatal("preview and main must not share a generated key")
	}
}

// secretsNames are the names these boots run under: a standing
// deployment's, by its key.
var secretsNames = docker.ByKey(idstest.N(1))

// boundSecrets is a deployment's secret bindings, for a test that expects
// them readable.
func boundSecrets(t *testing.T, a *App, projectID ids.ID, deployment string) map[string]secret.Binding {
	t.Helper()
	bindings, err := a.Secrets.Bindings.For(t.Context(), projectID, deployment)
	if err != nil {
		t.Fatal(err)
	}
	return bindings
}

// storedSecret is a value in the built-in store, "" when it holds none.
func storedSecret(t *testing.T, a *App, scope, name string) string {
	t.Helper()
	value, _, err := a.Secrets.Vault.Get(t.Context(), scope, name)
	if errors.Is(err, secret.ErrNotStored) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return value
}
