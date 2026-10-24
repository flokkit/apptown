package secret

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
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
	if got := runtimeValues(t, a); got["GITLAB_TOKEN"] != "glpat-remote" || got["OVERRIDE_ME"] != "local" {
		t.Fatal("remote resolution or file precedence changed")
	}
	if got, err := a.StepEnv(context.Background()); err != nil || len(got) != 1 || got[0] != "DEMO_KEY=step-value" {
		t.Fatalf("step secret boundary changed: %v %v", got, err)
	}
}

// runtimeValues is runtime lookup for a test that expects it to succeed.
func runtimeValues(t *testing.T, s *Service) map[string]string {
	t.Helper()
	values, err := s.WithContext(t.Context())
	if err != nil {
		t.Fatalf("runtime lookup: %v", err)
	}
	return values
}

// fileValues is the secrets file for a test that expects it readable.
func fileValues(t *testing.T, s *Service) map[string]string {
	t.Helper()
	values, err := s.readFileSecrets()
	if err != nil {
		t.Fatalf("reading the secrets file: %v", err)
	}
	return values
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

// A secrets file that cannot be read is an error wherever it is read,
// never an empty file: runtime lookup, the store list (which says whether
// Infisical is set up) and the external store a backup names.
//
// Kills: the unreadable file read as empty.
func TestAnUnreadableSecretsFileIsAnErrorNotEmpty(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// A folder where the file belongs: reading it fails, as an unreadable file does.
	if err := os.Mkdir(filepath.Join(dir, "secrets.env"), 0o700); err != nil {
		t.Fatal(err)
	}
	s := NewService(nil, Config{DataDir: dir})
	if values, err := s.WithContext(t.Context()); err == nil || values != nil {
		t.Fatalf("runtime lookup: %v %v", values, err)
	}
	if env, err := s.StepEnv(t.Context()); err == nil || env != nil {
		t.Fatalf("step secrets: %v %v", env, err)
	}
	if stores, err := s.Stores(); err == nil || stores != nil {
		t.Fatalf("stores: %v %v", stores, err)
	}
	if _, err := s.AvailableProviders(); err == nil {
		t.Fatal("available providers read an unreadable file as none")
	}
	if url, _, err := s.ExternalStore(); err == nil || url != "" {
		t.Fatalf("external store: %q %v", url, err)
	}
}

// An Infisical that answered once and now cannot be read fails runtime
// lookup with its reason, names the file would supply included: the
// values it answered before are not current. Masking still hides them.
//
// Kills: a failed refresh answering the last values; a failed refresh
// dropped from runtime lookup.
func TestAnInfisicalThatCannotBeReadFailsRuntimeLookup(t *testing.T) {
	t.Parallel()
	var down atomic.Bool
	s := instanceService(t, infisicalLines+"LOCAL=from-file\n", map[string]string{"TOKEN": "remote-token-value"}, &down)
	if got := runtimeValues(t, s); got["TOKEN"] != "remote-token-value" || got["LOCAL"] != "from-file" {
		t.Fatalf("first read: %v", got)
	}
	s.infisical.mu.Lock()
	s.infisical.fetched = time.Now().Add(-2 * time.Minute)
	s.infisical.mu.Unlock()
	down.Store(true)
	values, err := s.WithContext(t.Context())
	if err == nil || values != nil || !strings.Contains(err.Error(), "Infisical") || !strings.Contains(err.Error(), "503") {
		t.Fatalf("runtime lookup with Infisical down: %v %v", values, err)
	}
	if values, err := s.InfisicalValues(t.Context()); err == nil || values != nil {
		t.Fatalf("Infisical values with Infisical down: %v %v", values, err)
	}
	if got := s.Mask(t.Context(), "the token is remote-token-value"); strings.Contains(got, "remote-token-value") {
		t.Fatalf("masking lost the value Infisical answered before: %q", got)
	}
}
