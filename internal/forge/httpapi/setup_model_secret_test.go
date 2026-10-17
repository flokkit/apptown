package httpapi

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/forge/integration"
	"flokkit.io/forge/internal/forge/model"
	"flokkit.io/forge/internal/forge/secret"
	"flokkit.io/forge/internal/ids"
)

func TestSetupModelWritesSecrets(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	dir := t.TempDir()
	secrets := secret.NewService(db, secret.Config{DataDir: dir})
	models := model.New(db)
	h := NewSetupModel(secrets, integration.NewService(db, integration.Dependencies{Secrets: secrets}), models, db, nil)
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
	if slot, _, _ := model.NewSelection(models, "").Resolve(req.Context(), ids.Nil, ""); slot == "" {
		t.Fatal("a default model slot should exist after setup")
	}
}
