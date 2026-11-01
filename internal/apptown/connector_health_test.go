package apptown

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/httpapi"
	"github.com/flokkit/apptown/internal/apptown/integration"
)

// An integration's listed health says whether AppTown checked it and
// when, by the server's clock; a kind with no check reads as not checked,
// never as working.
func TestIntegrationHealthSaysWhetherAndWhenItWasChecked(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	for _, b := range []struct{ name, capability, provider, config string }{
		{"store", "objectstore", "minio", `{}`},
		{"secrets", integration.CapSecrets, "internal", `{}`},
	} {
		if _, err := a.Integrations.Bindings.Save(context.Background(), b.name, b.capability, b.provider, b.config, "fixture"); err != nil {
			t.Fatal(err)
		}
	}
	before := time.Now().Add(-time.Second)
	if err := a.Integrations.ObserveHealth(t.Context(), "secrets", connectorHealthFor(a)); err != nil {
		t.Fatal(err)
	}
	w := authzCall(a.Handler(), "boss", "GET", "/api/v1/integrations", "")
	if w.Code != 200 {
		t.Fatalf("integrations: %d %s", w.Code, w.Body.String())
	}
	var list httpapi.IntegrationList
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	got := map[string]integration.Health{}
	for _, e := range list.Integrations {
		got[e.Name] = e.Health
	}
	if h := got["store"]; h.Checked || h.OK || h.CheckedAt != "" {
		t.Fatalf("a kind with no check reads as checked: %+v", h)
	}
	h := got["secrets"]
	at, err := time.Parse(time.RFC3339, h.CheckedAt)
	if !h.Checked || !h.OK || err != nil || at.Before(before.Truncate(time.Second)) || at.After(time.Now().Add(time.Second)) {
		t.Fatalf("a checked kind has no server check time: %+v (%v)", h, err)
	}
	health := connectorHealthFor(a)
	if h := health.Health(t.Context(), integration.CapSCM, "gitlab", nil); h.StateLabel != integration.HealthNotSetUp || h.Checked || h.CheckedAt != "" {
		t.Fatalf("unconfigured GitLab is not a failed check: %+v", h)
	}
	if h := health.Health(t.Context(), integration.CapSecrets, "infisical", nil); h.StateLabel != integration.HealthNotSetUp || h.Checked {
		t.Fatalf("unconfigured Infisical must not be red: %+v", h)
	}
	if h := health.Test(t.Context(), "objectstore", "minio", map[string]any{}); h.Checked || h.OK {
		t.Fatalf("a test of a kind with no check passed: %+v", h)
	}
	if h := health.Health(t.Context(), integration.CapSecrets, "internal", nil); !h.Checked || h.CheckedAt == "" {
		t.Fatalf("a checked kind's reading is unstamped: %+v", h)
	}
}

// Testable is true exactly for the kinds whose Test connection asks the
// service with the configuration given: each answers a test of an empty
// configuration as checked, and the listing says so per entry,
// so the page never says such a kind cannot be checked.
func TestEveryKindSaysTrulyWhetherTestConnectionAsksIt(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	health := connectorHealthFor(a)
	if h := health.Health(t.Context(), integration.CapSCM, "gitlab", nil); h.StateLabel != integration.HealthNotSetUp || h.Checked || h.CheckedAt != "" {
		t.Fatalf("unconfigured GitLab is not a failed check: %+v", h)
	}
	for _, c := range integration.ConnectorCatalog() {
		if !health.Testable(c.Capability) {
			continue
		}
		if h, checked := health.test(t.Context(), c.Capability, c.Provider, map[string]any{}); !checked {
			t.Fatalf("%s says Test connection asks it, but an empty test is %+v (checked %v)", c.Capability, h, checked)
		}
	}
	if health.Testable("objectstore") || health.Testable(integration.CapSecrets) {
		t.Fatal("a kind tested only by its live check is not draft-tested")
	}
	if _, err := a.Integrations.Bindings.Save(context.Background(), "openrouter", integration.CapLLM, "openrouter", `{"provisioningKeyFrom":["OPENROUTER_PROVISIONING_KEY"],"maxLimit":5}`, "fixture"); err != nil {
		t.Fatal(err)
	}
	w := authzCall(a.Handler(), "boss", "GET", "/api/v1/integrations", "")
	var list httpapi.IntegrationList
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || w.Code != 200 {
		t.Fatalf("integrations: %d %v", w.Code, err)
	}
	for _, e := range list.Integrations {
		if e.Name == "openrouter" && (!e.Testable || e.Health.Checked) {
			t.Fatalf("AI models: testable %v, checked %v", e.Testable, e.Health.Checked)
		}
	}
}

// Listing and Test connection use the same authenticated registry request;
// an unchecked reading, anonymous success or refused credentials cannot pass.
func TestContainerRegistryHealthUsesStoredCredentials(t *testing.T) {
	a := testApp(t)
	if err := os.WriteFile(filepath.Join(a.cfg.dataDir, "secrets.env"), []byte("REGISTRY_TOKEN=stored-password\n"), 0600); err != nil {
		t.Fatal(err)
	}
	code, requests := 200, 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		user, password, ok := r.BasicAuth()
		if r.Method != "GET" || r.URL.Path != "/v2/" || !ok || user != "robot" || password != "stored-password" {
			t.Errorf("incorrect registry request: %s %s, authenticated %v", r.Method, r.URL.Path, ok)
		}
		if code == 302 {
			w.Header().Set("Location", "/somewhere-else")
		}
		w.WriteHeader(code)
	}))
	defer server.Close()
	original := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	defer func() { http.DefaultTransport = original }()
	cfg := map[string]any{"url": strings.TrimPrefix(server.URL, "https://"), "username": "robot", "tokenFrom": []string{"REGISTRY_TOKEN"}}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Integrations.Bindings.Save(t.Context(), "images", integration.CapContainerRegistry, "direct", string(raw), "fixture"); err != nil {
		t.Fatal(err)
	}
	for _, status := range []int{200, 401, 500, 302} {
		code = status
		before := requests
		if err := a.Integrations.ObserveHealth(t.Context(), "images", connectorHealthFor(a)); err != nil {
			t.Fatal(err)
		}
		afterCheck := requests
		w := authzCall(a.Handler(), "boss", "GET", "/api/v1/integrations", "")
		var list httpapi.IntegrationList
		if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || w.Code != 200 {
			t.Fatalf("listing: %d %v", w.Code, err)
		}
		var got integration.Health
		for _, row := range list.Integrations {
			if row.Name == "images" {
				got = row.Health
			}
		}
		if !got.Checked || got.CheckedAt == "" || got.OK != (status == 200) || requests != before+1 || requests != afterCheck {
			t.Fatalf("registry %d: %+v; requests %d", status, got, requests-before)
		}
		draft := connectorHealthFor(a).Test(t.Context(), integration.CapContainerRegistry, "direct", cfg)
		if !draft.Checked || draft.OK != (status == 200) {
			t.Fatalf("draft %d: %+v", status, draft)
		}
	}
	server.Close()
	got := connectorHealthFor(a).Test(t.Context(), integration.CapContainerRegistry, "direct", cfg)
	if !got.Checked || got.OK || !strings.Contains(got.Detail, "could not be reached") {
		t.Fatalf("unreachable: %+v", got)
	}
}

// Hosted registries challenge at /v2/ even with valid credentials. Only a
// same-domain HTTPS token service may receive the stored secret, without redirects.
func TestContainerRegistryHealthChecksBearerTokenService(t *testing.T) {
	a := testApp(t)
	if err := os.WriteFile(filepath.Join(a.cfg.dataDir, "secrets.env"), []byte("REGISTRY_TOKEN=stored-password\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, registry, realm string
		status                int
		want                  integration.HealthStatus
		detail                string
		tokenRequests         int
	}{
		{"digitalocean", "registry.digitalocean.com", "https://api.digitalocean.com/token", 200, integration.HealthPassed, "accepted", 1},
		{"github", "ghcr.io", "https://ghcr.io/token", 200, integration.HealthPassed, "accepted", 1},
		{"dockerhub", "registry-1.docker.io", "https://auth.docker.io/token", 200, integration.HealthPassed, "accepted", 1},
		{"gitlab", "registry.gitlab.com", "https://gitlab.com/token", 200, integration.HealthPassed, "accepted", 1},
		{"unauthorized", "registry.example.com", "https://auth.example.com/token", 401, integration.HealthFailed, "refused by the token service", 1},
		{"forbidden", "registry.example.com", "https://auth.example.com/token", 403, integration.HealthFailed, "refused by the token service", 1},
		{"failed", "registry.example.com", "https://auth.example.com/token", 503, integration.HealthFailed, "token service returned 503", 1},
		{"redirect", "registry.example.com", "https://auth.example.com/token", 302, integration.HealthFailed, "token service returned 302", 1},
		{"other domain", "registry.example.com", "https://auth.other.com/token", 200, integration.HealthUnknown, "Token service on another domain; not checked", 0},
		{"http", "registry.example.com", "http://auth.example.com/token", 200, integration.HealthUnknown, "HTTPS", 0},
		{"shared public suffix", "registry.example.co.uk", "https://auth.other.co.uk/token", 200, integration.HealthUnknown, "another domain", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registryRequests, tokenRequests := 0, 0
			tokenServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				tokenRequests++
				user, password, ok := r.BasicAuth()
				if r.Method != http.MethodGet || r.URL.Path != "/token" || !ok || user != "robot" || password != "stored-password" || r.URL.Query().Get("service") != tc.registry || r.URL.Query().Get("account") != "robot" {
					t.Errorf("incorrect token request: %s %s, authenticated %v", r.Method, r.URL, ok)
				}
				w.Header().Set("Location", tc.realm+"/redirected")
				w.WriteHeader(tc.status)
			}))
			t.Cleanup(tokenServer.Close)
			registryServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				registryRequests++
				user, password, ok := r.BasicAuth()
				if r.URL.Path != "/v2/" || !ok || user != "robot" || password != "stored-password" {
					t.Errorf("incorrect registry request: %s, authenticated %v", r.URL, ok)
				}
				w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm=%q,service=%q`, tc.realm, tc.registry))
				w.WriteHeader(http.StatusUnauthorized)
			}))
			t.Cleanup(registryServer.Close)
			transport := tokenServer.Client().Transport.(*http.Transport).Clone()
			// Both local fixtures use httptest's certificate for example.com.
			transport.TLSClientConfig.ServerName = "example.com"
			transport.Proxy = nil
			transport.DisableKeepAlives = true
			transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
				// Keep the request's real host for domain validation, while all
				// network effects go to these two local TLS fixtures.
				target := tokenServer.Listener.Addr().String()
				if addr == tc.registry+":443" && registryRequests == 0 {
					target = registryServer.Listener.Addr().String()
				}
				return (&net.Dialer{}).DialContext(ctx, network, target)
			}
			original := http.DefaultTransport
			http.DefaultTransport = transport
			t.Cleanup(func() { http.DefaultTransport = original; transport.CloseIdleConnections() })
			cfg := map[string]any{"url": tc.registry, "username": "robot", "tokenFrom": []string{"REGISTRY_TOKEN"}}
			got := connectorHealthFor(a).Test(t.Context(), integration.CapContainerRegistry, "direct", cfg)
			if !got.Checked || got.CheckedAt == "" || got.StateLabel != tc.want || got.OK != (tc.want == integration.HealthPassed) || !strings.Contains(got.Detail, tc.detail) {
				t.Errorf("unexpected health: %+v", got)
			}
			if registryRequests != 1 || tokenRequests != tc.tokenRequests {
				t.Errorf("requests: registry=%d token=%d; want 1, %d", registryRequests, tokenRequests, tc.tokenRequests)
			}
		})
	}
}

func TestPartialGitLabConfigurationNeedsAttention(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	if err := a.Secrets.SetInstance(t.Context(), "GITLAB_URL", "https://gitlab.invalid", "boss"); err != nil {
		t.Fatal(err)
	}
	h := connectorHealthFor(a).Health(t.Context(), integration.CapSCM, "gitlab", nil)
	if h.StateLabel != integration.HealthFailed || !h.Checked || !strings.Contains(h.Detail, "GITLAB_TOKEN") {
		t.Fatalf("partly configured GitLab must fail with the missing setting: %+v", h)
	}
}

func TestSavedIntegrationTestUpdatesAttentionWithoutProbingOnReads(t *testing.T) {
	a := testApp(t)
	a.addGrant("user", "boss", "", string(access.CapIntegrationConnector), access.ScopeInstance, "", "boss", "")
	if err := os.WriteFile(filepath.Join(a.cfg.dataDir, "secrets.env"), []byte("REGISTRY_TOKEN=stored-password\n"), 0600); err != nil {
		t.Fatal(err)
	}
	status, requests := http.StatusUnauthorized, 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++; w.WriteHeader(status) }))
	defer server.Close()
	previous := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	defer func() { http.DefaultTransport = previous }()
	config := mustJSON(map[string]any{"url": strings.TrimPrefix(server.URL, "https://"), "username": "robot", "tokenFrom": []string{"REGISTRY_TOKEN"}})
	if _, err := a.Integrations.Bindings.Save(t.Context(), "images", integration.CapContainerRegistry, "direct", config, "fixture"); err != nil {
		t.Fatal(err)
	}
	handler := a.Handler()
	test := func(want integration.HealthStatus) {
		t.Helper()
		w := authzCall(handler, "boss", http.MethodPost, "/api/v1/integrations/images/test", `{}`)
		var h integration.Health
		if err := json.Unmarshal(w.Body.Bytes(), &h); err != nil || w.Code != 200 || h.StateLabel != want || h.CheckedAt == "" {
			t.Fatalf("saved test: %d %+v %v %s", w.Code, h, err, w.Body.String())
		}
	}
	test(integration.HealthFailed)
	before := requests
	w := authzCall(handler, "boss", http.MethodGet, "/api/v1/integrations", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"stateLabel":"failed"`) || requests != before {
		t.Fatalf("list did not reuse saved result: %d requests=%d/%d %s", w.Code, requests, before, w.Body.String())
	}
	w = authzCall(handler, "boss", http.MethodGet, "/api/v1/attention", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"key":"integrations/images"`) || requests != before {
		t.Fatalf("attention did not reuse failure: %d requests=%d/%d %s", w.Code, requests, before, w.Body.String())
	}
	status = http.StatusOK
	test(integration.HealthPassed)
	before = requests
	w = authzCall(handler, "boss", http.MethodGet, "/api/v1/attention", "")
	var attention struct {
		Items []struct {
			Key string `json:"key"`
		} `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &attention); err != nil {
		t.Fatal(err)
	}
	for _, item := range attention.Items {
		if item.Key == "integrations/images" {
			t.Fatalf("repair retained integration attention: %s", w.Body.String())
		}
	}
	if w.Code != 200 || requests != before {
		t.Fatalf("repair did not clear attention: %d %s", w.Code, w.Body.String())
	}
	w = authzCall(handler, "outsider", http.MethodPost, "/api/v1/integrations/images/test", `{}`)
	if w.Code != http.StatusForbidden || requests != before {
		t.Fatalf("unauthorized test reached provider: %d requests=%d/%d", w.Code, requests, before)
	}
}
