package secret

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestInfisicalResolution(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	a := NewService(nil, Config{DataDir: dir})
	runtimeInfisicalFake(t, dir, map[string]string{"GITLAB_TOKEN": "glpat-remote", "STEP_DEMO_KEY": "step-value", "OVERRIDE_ME": "remote"})
	path := filepath.Join(dir, "secrets.env")
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = file.WriteString("OVERRIDE_ME=local\n")
	file.Close()
	if err != nil {
		t.Fatal(err)
	}
	if a.Value(context.Background(), "GITLAB_TOKEN") != "glpat-remote" || a.Value(context.Background(), "OVERRIDE_ME") != "local" {
		t.Fatal("remote resolution or file precedence changed")
	}
	if got := a.StepEnv(context.Background()); len(got) != 1 || got[0] != "DEMO_KEY=step-value" {
		t.Fatalf("step secret boundary changed: %v", got)
	}
}

func runtimeInfisicalFake(t *testing.T, dir string, keys map[string]string) {
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
	if err := os.WriteFile(filepath.Join(dir, "secrets.env"), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
}
