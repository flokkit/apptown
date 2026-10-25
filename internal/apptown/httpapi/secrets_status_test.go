package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/secret"
)

func TestSecretsAPI(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	dir := t.TempDir()
	secrets := secret.NewService(db, secret.Config{DataDir: dir})
	projects := project.New(db, nil)
	projectID := httpTestProject(t, db, "p", "Project")
	h := NewSecrets(secrets, integration.NewService(db, integration.Dependencies{Secrets: secrets}), projects, nil, db, dir, nil)
	repo := filepath.Join(dir, "workspaces", "project-"+projectID.String())
	if err := os.MkdirAll(filepath.Join(repo, ".apptown"), 0o755); err != nil {
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
	if err := os.WriteFile(filepath.Join(repo, ".apptown", "app.yaml"), []byte(contract), 0o644); err != nil {
		t.Fatal(err)
	}
	spec, err := os.ReadFile("../secret/testdata/declaration.toml")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, secret.SpecFile), spec, 0o644); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/projects/{slug}/secrets", h.ProjectSecrets)
	mux.HandleFunc("PUT /api/v1/projects/{slug}/secrets/{deployment}/{name}", h.BindSecret)
	mux.HandleFunc("PUT /api/v1/projects/{slug}/secrets/{deployment}", h.SetSecretProfile)
	mux.HandleFunc("DELETE /api/v1/projects/{slug}/secrets/{deployment}/{name}", h.UnbindSecret)
	call := func(method, path, body string) (int, map[string]any) {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req = req.WithContext(access.WithCaller(req.Context(), access.Caller{Principal: access.Principal{Kind: "user", Name: "bls", Via: "session"}, Verified: true, Receipt: "bls"}))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	code, out := call(http.MethodGet, "/api/v1/projects/p/secrets", "")
	if code != 200 || out["declared"] != true {
		t.Fatalf("GET: %d %v", code, out)
	}
	envs := out["deployments"].([]any)
	main := envs[1].(map[string]any)
	if main["name"] != "main" || main["profile"] != "default" {
		t.Fatalf("main env: %v", main)
	}
	if miss := secretStatusJSON(main["missing"]); !strings.Contains(miss, "STRIPE_API_KEY") || !strings.Contains(miss, "MIGRATION_DB_ROLE") || strings.Contains(miss, "SECRET_KEY_BASE") {
		t.Fatalf("missing in main: %s", miss)
	}
	if prev := envs[0].(map[string]any); prev["profile"] != "preview" {
		t.Fatalf("preview takes the preview profile: %v", prev["profile"])
	}
	// Bind a value: the response never carries it, and the store holds it obfuscated.
	code, out = call(http.MethodPut, "/api/v1/projects/p/secrets/main/STRIPE_API_KEY", `{"store":"internal","value":"sk_live_abc"}`)
	if code != 200 || out["bound"] != true || out["store"] != "internal" || strings.Contains(secretStatusJSON(out), "sk_live") {
		t.Fatalf("PUT: %d %v", code, out)
	}
	_, out = call(http.MethodGet, "/api/v1/projects/p/secrets", "")
	if strings.Contains(secretStatusJSON(out), "sk_live") {
		t.Fatal("a value leaked into the status view")
	}
	main = out["deployments"].([]any)[1].(map[string]any)
	var stripe map[string]any
	for _, s := range main["secrets"].([]any) {
		if s.(map[string]any)["name"] == "STRIPE_API_KEY" {
			stripe = s.(map[string]any)
		}
	}
	if stripe["bound"] != true || stripe["version"] != "1" || stripe["updatedBy"] != "bls" {
		t.Fatalf("stripe status: %v", stripe)
	}
	// Infisical is not set up here: binding to it is refused with a sentence.
	if code, out = call(http.MethodPut, "/api/v1/projects/p/secrets/main/MIGRATION_DB_ROLE", `{"store":"infisical","ref":"ROLE"}`); code != 409 {
		t.Fatalf("infisical unavailable: %d %v", code, out)
	}
	// Profile choice is validated against the declaration.
	if code, _ = call(http.MethodPut, "/api/v1/projects/p/secrets/main", `{"profile":"nope"}`); code != 422 {
		t.Fatalf("unknown profile: %d", code)
	}
	if code, _ = call(http.MethodPut, "/api/v1/projects/p/secrets/main", `{"profile":"preview"}`); code != 200 {
		t.Fatalf("set profile: %d", code)
	}
	// Unbind removes the binding and the stored value.
	if code, _ = call(http.MethodDelete, "/api/v1/projects/p/secrets/main/STRIPE_API_KEY", ""); code != 200 {
		t.Fatalf("unbind: %d", code)
	}
	if _, _, err := secrets.Vault.Get(t.Context(), secret.Scope("p", "main", ""), "STRIPE_API_KEY"); !errors.Is(err, secret.ErrNotStored) {
		t.Fatalf("unbind must remove the stored value: %v", err)
	}
	// Bad names and unknown deployments are refused.
	if code, _ = call(http.MethodPut, "/api/v1/projects/p/secrets/main/not-a-name", `{"value":"x"}`); code != 400 {
		t.Fatalf("bad name: %d", code)
	}
	if code, _ = call(http.MethodPut, "/api/v1/projects/p/secrets/prod/X", `{"value":"x"}`); code != 404 {
		t.Fatalf("unknown env: %d", code)
	}
	// The project's stores list is honest about the built-in store.
	code, out = call(http.MethodGet, "/api/v1/projects/p/secrets", "")
	if code != 200 || !strings.Contains(secretStatusJSON(out), "not a vault") {
		t.Fatalf("stores: %d %s", code, secretStatusJSON(out))
	}
}
func secretStatusJSON(value any) string { raw, _ := json.Marshal(value); return string(raw) }
