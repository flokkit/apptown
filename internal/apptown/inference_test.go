package apptown

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/agentenv"
	"github.com/flokkit/apptown/internal/apptown/inference"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/execution"

	"github.com/flokkit/apptown/internal/apptown/httpapi"
)

func TestInferenceConfigurationAndRuntimeSecrets(t *testing.T) {
	t.Parallel()
	a := testApp(t, Config{InferenceURL: "http://worker:7779", InferenceNetworks: []string{"192.0.2.0/24"}})
	for _, nets := range [][]string{nil, {""}, {"broken"}, {"0.0.0.0/0"}, {"::/0"}, {"192.0.2.0/24", "bad"}} {
		if (Config{InferenceURL: "http://worker:7779", InferenceNetworks: nets}).Validate() == nil {
			t.Errorf("accepted invalid allowlist %v", nets)
		}
	}
	if agentenv.RequireInferenceRuntime(a.inference != nil, execution.Local) == nil {
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
	httpapi.NewIntegrationListing(a.Integrations, a.Deployment, connectorHealthFor(a), a.Access).ListIntegrations(w, httptest.NewRequest("GET", "/api/v1/integrations", nil))
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
				if _, err := integration.NewWithClock(db, nil).Save(context.Background(), "paid", "model", "pi", string(config), "fixture"); err != nil {
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

// Serial: routes api.openai.com to a local TLS fixture via DefaultTransport.
// The saved preset uses the real built-in URL, never an endpoint override.
func TestOpenAIConnectionDiscoversModelsAndRendersPiConfiguration(t *testing.T) {
	a := testApp(t)
	status, probes := http.StatusOK, 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probes++
		if r.Host != "api.openai.com" || r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer openai-connection-canary" {
			t.Errorf("unexpected OpenAI request: %s %s", r.Host, r.URL.Path)
		}
		w.WriteHeader(status)
		fmt.Fprint(w, `{"data":[{"id":"gpt-5.6-luna"},{"id":"gpt-5.6-terra"},{"id":"gpt-5.6-sol"}]}`)
	}))
	defer server.Close()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialTLSContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr != "api.openai.com:443" {
			return nil, fmt.Errorf("unexpected upstream %s", addr)
		}
		// Only the local fixture is dialed, with its own test TLS settings.
		return (&tls.Dialer{Config: server.Client().Transport.(*http.Transport).TLSClientConfig}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	original := http.DefaultTransport
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = original; transport.CloseIdleConnections() })
	if err := a.Secrets.SetInstance(t.Context(), "OPENAI_API_KEY", "openai-connection-canary", "fixture"); err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	for _, preset := range integration.FindConnector(integration.CapModel, "pi").Presets {
		if preset.Name == "openai" {
			config = maps.Clone(preset.Config)
		}
	}
	if config == nil {
		t.Fatal("Connect is missing OpenAI")
	}
	// A connected built-in provider explicitly enables its chosen models.
	config["model"] = "openai/gpt-5.6-sol"
	config["models"] = []string{"gpt-5.6-luna", "gpt-5.6-terra", "gpt-5.6-sol"}
	config["modelsJson"] = `[{"id":"gpt-5.6-luna"},{"id":"gpt-5.6-terra"},{"id":"gpt-5.6-sol"}]`
	health := connectorHealthFor(a)
	for _, code := range []int{http.StatusOK, http.StatusUnauthorized} {
		status = code
		before := probes
		h := health.Test(t.Context(), integration.CapModel, "pi", config)
		if !h.Checked || h.OK != (code == http.StatusOK) || probes != before+1 {
			t.Fatalf("Test connection for HTTP %d: %+v, probes %d", code, h, probes-before)
		}
	}
	status = http.StatusOK
	if _, err := a.Integrations.Bindings.Save(t.Context(), "openai", integration.CapModel, "pi", mustJSON(config), "fixture"); err != nil {
		t.Fatal(err)
	}
	ids, err := a.providers.Discover(t.Context(), "openai", config)
	if err != nil || !slices.Equal(ids, []string{"gpt-5.6-luna", "gpt-5.6-sol", "gpt-5.6-terra"}) {
		t.Fatalf("discovery: %v %v", ids, err)
	}
	entries, unlisted, err := a.providers.Models(t.Context())
	if err != nil || len(unlisted) != 0 {
		t.Fatalf("slot models: %+v %v", unlisted, err)
	}
	for _, id := range ids {
		found := false
		for _, entry := range entries {
			found = found || entry.Pattern == "openai/"+id
		}
		if !found {
			t.Errorf("slot picker lacks openai/%s", id)
		}
	}
	raw, err := inferenceRuntimeFor(a).ModelsJSON(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Providers map[string]struct {
			API, BaseURL, APIKey string
			Models               []struct{ ID string }
		}
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	p := cfg.Providers["openai"]
	if p.API != "openai-responses" || !strings.HasSuffix(p.BaseURL, "/providers/openai") || p.APIKey != "$APPTOWN_SESSION_TOKEN" || len(p.Models) != 3 || strings.Contains(string(raw), "openai-connection-canary") {
		t.Fatalf("pi configuration does not preserve the OpenAI provider/model boundary: %s", raw)
	}
	var rendered []string
	for _, m := range p.Models {
		rendered = append(rendered, m.ID)
	}
	slices.Sort(rendered)
	if !slices.Equal(rendered, ids) {
		t.Fatalf("Pi models %v differ from discovered models %v", rendered, ids)
	}

	// Clearing enablement must remove every model from Pi configuration,
	// even while the provider retains its model definitions.
	delete(config, "model")
	config["models"] = []string{}
	if _, err := a.Integrations.Bindings.Save(t.Context(), "openai", integration.CapModel, "pi", mustJSON(config), "fixture"); err != nil {
		t.Fatal(err)
	}
	before := probes
	raw, err = inferenceRuntimeFor(a).ModelsJSON(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	cfg.Providers = nil
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Providers["openai"].Models) != 0 || probes != before {
		t.Fatalf("empty enabled list rendered models or probed upstream: %s, probes %d", raw, probes-before)
	}
}
