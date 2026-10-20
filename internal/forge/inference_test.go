package forge

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flokkit.io/forge/internal/forge/agentenv"
	"flokkit.io/forge/internal/forge/inference"
	"flokkit.io/forge/internal/forge/integration"
	"flokkit.io/forge/internal/forge/secret"

	"flokkit.io/forge/internal/forge/httpapi"
)

func TestInferenceConfigurationAndRuntimeSecrets(t *testing.T) {
	t.Parallel()
	a := testApp(t, Config{InferenceURL: "http://worker:7779", InferenceNetworks: []string{"192.0.2.0/24"}})
	for _, nets := range [][]string{nil, {""}, {"broken"}, {"0.0.0.0/0"}, {"::/0"}, {"192.0.2.0/24", "bad"}} {
		if (Config{InferenceURL: "http://worker:7779", InferenceNetworks: nets}).Validate() == nil {
			t.Errorf("accepted invalid allowlist %v", nets)
		}
	}
	if agentenv.RequireInferenceRuntime(a.inference != nil, ExecutorLocal) == nil {
		t.Fatal("local model runtime accepted")
	}
	if _, err := a.Integrations.Bindings.Save(context.Background(), "paid", "model", "pi", `{"baseUrl":"http://upstream.invalid/v1","apiKeyFrom":["PAID_KEY"],"modelsJson":"[{\"id\":\"allowed\",\"headers\":{\"Authorization\":\"provider-canary\"}}]"}`, "fixture"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.cfg.dataDir, "secrets.env"), []byte("PAID_KEY=provider-canary\n"), 0600); err != nil {
		t.Fatal(err)
	}
	raw, err := inferenceRuntimeFor(a).ModelsJSON(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "provider-canary") || strings.Contains(string(raw), "upstream.invalid") || !strings.Contains(string(raw), "$APPTOWN_SESSION_TOKEN") || !strings.Contains(string(raw), "http://worker:7779/providers/paid") {
		t.Fatalf("unsafe runtime config: %s", raw)
	}
	if err := a.providers.CheckEnvironment(context.Background(), []string{"ALIAS=provider-canary"}); err == nil || strings.Contains(err.Error(), "provider-canary") {
		t.Fatal("provider credential alias was accepted or printed")
	}
	w := httptest.NewRecorder()
	httpapi.NewIntegrationListing(a.Integrations, a.Deployment, connectorHealthFor(a), &a.docker, agentenv.RegistryFormats(), nil).ListIntegrations(w, httptest.NewRequest("GET", "/api/v1/integrations", nil))
	if w.Code != 200 || strings.Contains(w.Body.String(), "provider-canary") {
		t.Fatal("legacy model headers escaped through the integration API")
	}
	var v map[string]any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
}

func TestInferenceRuntimeRejectsCredentialMetadata(t *testing.T) {
	t.Parallel()
	// Carries the three characters JSON escapes. A scan of rendered bytes
	// misses a credential like this; the value-level check must not.
	const canaryKey = "metadata-<canary>&"
	placements := map[string]map[string]any{
		// A whitelisted field that survives the Pi projection.
		"value": {"id": "allowed", "name": canaryKey},
		// Only a map key carries it; the key walk must find it on its own.
		"key": {"id": "allowed", "name": "allowed", "compat": map[string]any{canaryKey: true}},
	}
	for _, credential := range []string{"apiKey", "header"} {
		for placement, model := range placements {
			t.Run(credential+"/"+placement, func(t *testing.T) {
				t.Parallel()
				db := testDB(t)
				catalog := inference.NewCatalog(db, secret.NewService(db, secret.Config{DataDir: t.TempDir()}))
				def := map[string]any{
					"baseUrl": "http://upstream.invalid/v1", "api": "openai-completions",
					"models": []any{model},
				}
				if credential == "apiKey" {
					def["apiKey"] = canaryKey
				} else {
					def["headers"] = map[string]any{"Authorization": canaryKey}
				}
				config, err := json.Marshal(map[string]any{"providerDef": def})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := integration.New(db).Save(context.Background(), "paid", "model", "pi", string(config), "fixture"); err != nil {
					t.Fatal(err)
				}
				raw, err := piModelConfiguration{catalog: catalog, baseURL: "http://host.docker.internal:7779"}.ModelsJSON(context.Background())
				if err == nil || raw != nil {
					t.Fatal("credential-bearing model metadata was released")
				}
				if !strings.Contains(err.Error(), "places a credential in public model metadata") {
					t.Fatal("configuration failed without the expected credential refusal")
				}
				if strings.Contains(err.Error(), canaryKey) {
					t.Fatal("credential leaked through the refusal")
				}
			})
		}
	}
}
