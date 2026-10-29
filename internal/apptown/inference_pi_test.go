//go:build pi

package apptown

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/conversation"
	"github.com/flokkit/apptown/internal/console/consoletest"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
	"github.com/flokkit/apptown/internal/testinputs"
)

// Uses the pinned harness's actual model runtime and native SDKs, not a
// handcrafted approximation of their URL/auth conventions. No real provider.
func TestInferencePinnedPi(t *testing.T) {
	module := os.Getenv("APPTOWN_PI_MODULE")
	if module == "" {
		t.Skip("set APPTOWN_PI_MODULE to Pi 0.84.3's package directory")
	}
	var token string
	var reached atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Add(1)
		if r.Header.Get("Authorization") != "Bearer upstream-canary" && r.Header.Get("X-Api-Key") != "upstream-canary" && r.Header.Get("X-Goog-Api-Key") != "upstream-canary" {
			t.Error("missing trusted upstream credential")
		}
		if token != "" && strings.Contains(fmt.Sprint(r.Header), token) {
			t.Error("AppTown token leaked upstream")
		}
		http.Error(w, "upstream-canary", 418)
	}))
	defer upstream.Close()
	a := testApp(t, Config{InferenceURL: "http://placeholder:7779", InferenceNetworks: []string{"127.0.0.1/32"}, ConsoleAgent: consoletest.NewAgent()})

	// The gateway admits a person's workload only for the principal it was
	// started by, whose account is enabled.
	testAccounts(t, a.DB, "test")
	m := consoleMetaFor("pi-chat", idstest.N(7), ids.Nil)
	m.Actor, m.Token = "test", "pi-chat-secret"
	m.Labels[conversation.OpenerLabel] = testPrincipalOf(t, a.DB, "test").String()
	token = a.Console.Restore(m).Token
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
	testinputs.Files(t, "testdata/inference_pi.mjs")
	cmd := exec.Command(strings.TrimSpace(string(found.Output)), "testdata/inference_pi.mjs")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "APPTOWN_PI_MODULE=" + module, "APPTOWN_PI_CONFIG=" + config, "APPTOWN_PI_PROXY=" + proxy.URL, "APPTOWN_SESSION_TOKEN=" + token}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("pinned Pi: %v\n%s", err, out)
	} else {
		t.Log(string(out))
	}
	// Every SDK's request crossed the proxy to the provider (an SDK may
	// retry the provider's refusal, so at least one each).
	if seen := int(reached.Load()); seen < len(configs) {
		t.Fatalf("not all SDKs reached the provider through the proxy: %d", seen)
	}
}
