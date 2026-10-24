package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/model"
	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/ids"
)

func TestSetupModelWritesSecrets(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	dir := t.TempDir()
	secrets := secret.NewService(db, secret.Config{DataDir: dir})
	models := model.New(db)
	h := NewSetupModel(secrets, integration.NewService(db, integration.Dependencies{Secrets: secrets}), models, db, nil)
	h.anthropicBase = "http://127.0.0.1:1" // no test reaches Anthropic
	req := httptest.NewRequest("POST", "/api/v1/setup/model", strings.NewReader(`{"provider":"anthropic","key":"sk-ant-test-123"}`))
	req = req.WithContext(access.WithCaller(context.Background(), access.Caller{Principal: access.Principal{Kind: "user", Name: "bls", Via: "session"}, Verified: true}))
	w := httptest.NewRecorder()
	h.SetupModel(w, req)
	if w.Code != 200 {
		t.Fatalf("setup: %d %s", w.Code, w.Body.String())
	}
	envPath := filepath.Join(dir, "secrets.env")
	b, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "ANTHROPIC_API_KEY=sk-ant-test-123") {
		t.Fatalf("key not written: %s", b)
	}
	if fi, _ := os.Stat(envPath); fi.Mode().Perm() != 0o600 {
		t.Fatalf("secrets.env must be 0600, is %v", fi.Mode().Perm())
	}
	if slot, _, _ := model.NewSelection(models).Resolve(req.Context(), ids.Nil, ""); slot == "" {
		t.Fatal("a default model slot should exist after setup")
	}
	// Replacing the key changes the credential in use: the request is on the
	// record before the file changes, and the outcome after.
	again := func(key string) int {
		r := httptest.NewRequest("POST", "/api/v1/setup/model", strings.NewReader(`{"provider":"anthropic","key":"`+key+`"}`))
		w := httptest.NewRecorder()
		h.SetupModel(w, r.WithContext(req.Context()))
		return w.Code
	}
	keyIs := func(want string) {
		t.Helper()
		if b, err := os.ReadFile(envPath); err != nil || !strings.Contains(string(b), "ANTHROPIC_API_KEY="+want+"\n") {
			t.Fatalf("the key in the file is not %s: %v", want, err)
		}
	}
	restore := db.FailNextStatement("-- name: AppendReceipt")
	code := again("sk-ant-unrecorded")
	restore()
	if code == 200 {
		t.Fatal("a key change whose request was not recorded succeeded")
	}
	keyIs("sk-ant-test-123")
	restore = db.FailStatementAfter("-- name: AppendReceipt", 1)
	code = again("sk-ant-test-456")
	restore()
	if code == 200 {
		t.Fatal("a key change whose outcome was not recorded succeeded")
	}
	// A file that cannot be read is not rewritten (a directory in its
	// place fails for root too).
	if err := os.Rename(envPath, envPath+".kept"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(envPath, 0o700); err != nil {
		t.Fatal(err)
	}
	code = again("sk-ant-unwritable")
	if err := os.Remove(envPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(envPath+".kept", envPath); err != nil {
		t.Fatal(err)
	}
	if code == 200 {
		t.Fatal("a key that could not be written succeeded")
	}
	keyIs("sk-ant-test-456")
}

// A first OpenRouter key sets the default model to what OpenRouter's
// latest-Sonnet alias names in its model list at setup, as a concrete id the
// inference gateway admits (it refuses "~" selectors), never a fixed version
// that goes stale (the desktop trial, 20 Oct 2026, got claude-sonnet-4.5).
// When the list cannot be read it falls back to a fixed current id.
//
// Kills: a fixed default, the alias itself stored, or no fallback.
func TestSetupModelDefaultsOpenRouterToWhereItsLatestSonnetPoints(t *testing.T) {
	t.Parallel()
	catalog := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"anthropic/claude-sonnet-9"},{"id":"~anthropic/claude-sonnet-latest","alias_target":{"slug":"anthropic/claude-sonnet-9"}}]}`))
	}))
	t.Cleanup(catalog.Close)
	for name, c := range map[string]struct{ url, want string }{
		"the list answers":        {catalog.URL, "openrouter/anthropic/claude-sonnet-9"},
		"the list cannot be read": {catalog.URL + "/missing", openRouterFallback},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			db := databaseSuite.New(t)
			secrets := secret.NewService(db, secret.Config{DataDir: t.TempDir()})
			models := model.New(db)
			h := NewSetupModel(secrets, integration.NewService(db, integration.Dependencies{Secrets: secrets}), models, db, nil)
			h.openRouterBase = c.url
			req := httptest.NewRequest("POST", "/api/v1/setup/model", strings.NewReader(`{"provider":"openrouter","key":"sk-or-test-123"}`))
			req = req.WithContext(access.WithCaller(context.Background(), access.Caller{Principal: access.Principal{Kind: "user", Name: "bls", Via: "session"}, Verified: true}))
			w := httptest.NewRecorder()
			h.SetupModel(w, req)
			if w.Code != 200 {
				t.Fatalf("setup: %d %s", w.Code, w.Body.String())
			}
			if slot, _, _ := model.NewSelection(models).Resolve(req.Context(), ids.Nil, ""); slot != c.want {
				t.Fatalf("the default model is %q, want %q", slot, c.want)
			}
		})
	}
}

// A first Anthropic key sets the default model to the newest Sonnet in
// Anthropic's own list, asked with that key: Anthropic has no "latest"
// alias, and the old default anthropic/claude-sonnet-latest named no model
// (every chat failed). Anthropic lists newest first, so the first Sonnet is
// taken. When the list cannot be read it falls back to a fixed current id.
//
// Kills: the old fixed default; taking a later Sonnet than the first; no
// fallback; the key not sent.
func TestSetupModelDefaultsAnthropicToItsNewestListedSonnet(t *testing.T) {
	t.Parallel()
	list := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Header.Get("X-Api-Key") != "sk-ant-test-123" || r.Header.Get("Anthropic-Version") == "" {
			http.Error(w, `{"type":"error"}`, http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-haiku-9"},{"id":"claude-sonnet-9"},{"id":"claude-opus-9"},{"id":"claude-sonnet-8"}]}`))
	}))
	t.Cleanup(list.Close)
	for name, c := range map[string]struct{ url, want string }{
		"the list answers":        {list.URL, "anthropic/claude-sonnet-9"},
		"the list cannot be read": {list.URL + "/missing", anthropicFallback},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			db := databaseSuite.New(t)
			secrets := secret.NewService(db, secret.Config{DataDir: t.TempDir()})
			models := model.New(db)
			h := NewSetupModel(secrets, integration.NewService(db, integration.Dependencies{Secrets: secrets}), models, db, nil)
			h.anthropicBase = c.url
			req := httptest.NewRequest("POST", "/api/v1/setup/model", strings.NewReader(`{"provider":"anthropic","key":"sk-ant-test-123"}`))
			req = req.WithContext(access.WithCaller(context.Background(), access.Caller{Principal: access.Principal{Kind: "user", Name: "bls", Via: "session"}, Verified: true}))
			w := httptest.NewRecorder()
			h.SetupModel(w, req)
			if w.Code != 200 {
				t.Fatalf("setup: %d %s", w.Code, w.Body.String())
			}
			if slot, _, _ := model.NewSelection(models).Resolve(req.Context(), ids.Nil, ""); slot != c.want {
				t.Fatalf("the default model is %q, want %q", slot, c.want)
			}
		})
	}
}
