//go:build pi

package forge

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/forge/identity"
)

// Uses the pinned harness's actual model runtime and native SDKs, not a
// handcrafted approximation of their URL/auth conventions. No real provider.
func TestInferencePinnedPi(t *testing.T) {
	module := os.Getenv("FORGE_PI_MODULE")
	if module == "" {
		t.Skip("set FORGE_PI_MODULE to Pi 0.84.3's package directory")
	}
	var token string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer upstream-canary" && r.Header.Get("X-Api-Key") != "upstream-canary" && r.Header.Get("X-Goog-Api-Key") != "upstream-canary" {
			t.Error("missing trusted upstream credential")
		}
		if token != "" && strings.Contains(fmt.Sprint(r.Header), token) {
			t.Error("AppTown token leaked upstream")
		}
		http.Error(w, "upstream-canary", 418)
	}))
	defer upstream.Close()
	a := testApp(t, Config{InferenceURL: "http://placeholder:7779", InferenceNetworks: []string{"127.0.0.1/32"}})

	// The gateway admits a person's workload only for the principal it was
	// started by, whose account is enabled.
	testAccounts(t, a.DB, "test")
	token = a.runTok.Mint(identity.Workload{Kind: "assist", Actor: "test", Principal: testPrincipalOf(t, a.DB, "test")})
	defer a.runTok.Revoke(token)
	configs := map[string]string{"openrouter": "openai-completions", "openai": "openai-responses", "anthropic": "anthropic-messages", "google": "google-generative-ai", "custom": "openai-completions"}
	for name, api := range configs {
		cfg := map[string]any{"model": name + "/*", "providerDef": map[string]any{"baseUrl": upstream.URL, "api": api, "apiKey": "upstream-canary"}}
		if name == "custom" {
			cfg["modelsJson"] = `[{"id":"allowed"}]`
		}
		if _, err := a.Integrations.Bindings.Save(context.Background(), name, "model", "pi", mustJSON(cfg), "fixture"); err != nil {
			t.Fatal(err)
		}
	}
	proxy := httptest.NewServer(a.InferenceHandler())
	defer proxy.Close()
	raw, err := piModelConfiguration{catalog: a.providers, baseURL: proxy.URL}.ModelsJSON(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	config := filepath.Join(home, "models.json")
	if err := os.WriteFile(config, raw, 0600); err != nil {
		t.Fatal(err)
	}
	// The node binary itself, resolved with this process's environment: a
	// version manager's shim (mise's, on a scheduled run's PATH) reads its
	// settings under HOME, which Pi's run replaces.
	found := (execution.Runner{}).Run(t.Context(), execution.Local, execution.Request{Argv: []string{"node", "-p", "process.execPath"}, Env: os.Environ(), Timeout: time.Minute})
	if found.Termination != execution.Exited || found.ExitCode != 0 {
		t.Fatalf("finding node: %s %d %v\n%s", found.Termination, found.ExitCode, found.RunErr, found.Output)
	}
	cmd := exec.Command(strings.TrimSpace(string(found.Output)), "testdata/inference_pi.mjs")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "FORGE_PI_MODULE=" + module, "FORGE_PI_CONFIG=" + config, "FORGE_PI_PROXY=" + proxy.URL, "FORGE_SESSION_TOKEN=" + token}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("pinned Pi: %v\n%s", err, out)
	} else {
		t.Log(string(out))
	}
	var seen int
	// sql-guarantee: inference_requests is written by the gateway and read back by no owner per request; each provider's rejected request receipt is the guarantee
	if err := a.DB.QueryRow(`SELECT COUNT(DISTINCT provider) FROM inference_requests WHERE actor='test' AND outcome='rejected' AND http_status=418`).Scan(&seen); err != nil {
		t.Fatal(err)
	}
	if seen != len(configs) {
		t.Fatalf("not all SDKs reached the proxy: %d", seen)
	}
}
