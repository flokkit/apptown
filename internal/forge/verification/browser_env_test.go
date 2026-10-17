package verification

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flokkit.io/forge/internal/check"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/ids"
)

// Whether the browser check applies comes from the app's contract, not from
// what the app answers: a server app's SPA fallback, redirect or login page is
// never judged as a static site's file.
func TestAServerAppIsNotJudgedByTheBrowserCheck(t *testing.T) {
	t.Parallel()
	for _, status := range []int{200, 302, 401} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				w.Write([]byte(`<html>Server-rendered app or SPA fallback</html>`))
			}))
			defer server.Close()
			dir := t.TempDir()
			if err := os.MkdirAll(filepath.Join(dir, ".forge"), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, ".forge/app.yaml"), []byte("app:\n  web:\n    image: example/server:1\n    public: true\n    ports: [8080]\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if got := (Runtime{}).Probe(context.Background(), Target{Naked: server.URL, ContractDir: dir}, "browser-env"); got.Outcome != check.Conforms {
				t.Fatalf("server app misclassified: %+v", got)
			}
		})
	}
}

// A standing deployment is judged by the contract of the revision it runs.
// When that revision cannot be read, or none is recorded, the answer is
// unknown and the forge's: the working tree says nothing about what runs.
func TestAStandingBrowserCheckDoesNotSubstituteAnUnreadableRelease(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	repo := filepath.Join(dir, "workspaces", "project-demo")
	if err := os.MkdirAll(filepath.Join(repo, ".forge"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".forge", "app.yaml"), []byte("app:\n  web: {image: 'example/server:1', public: true, ports: [8080]}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, sha := range []string{"unavailable-release", ""} {
		got := (Runtime{dataDir: dir}).Probe(context.Background(), Target{Slug: "demo", ReleaseSHA: sha, Naked: "http://127.0.0.1:1"}, "browser-env")
		if got.Outcome != check.CannotAssess || !got.Fault {
			t.Fatalf("release %q was judged by the working tree: %+v", sha, got)
		}
	}
}

// A container replaced between the two reads of what it was started with is
// unknown and the forge's, never a verdict about the wrong container.
func TestAContainerReplacedDuringTheBrowserCheckIsUnknown(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".forge"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".forge/app.yaml"), []byte("app:\n  web: {static: ., public: true}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == check.BrowserEnvPath {
			w.Write([]byte(docker.BrowserEnvScript(`{"KEY":"value"}`)))
			return
		}
		w.Write([]byte(`<head><script src="/_forge/env.js"></script><script src="app.js"></script></head>`))
	}))
	defer server.Close()
	calls := 0
	dk := docker.Scope{Client: dockerClientFunc(func(_ context.Context, _ string, c docker.Command) ([]byte, error) {
		calls++
		id := "before"
		if calls > 1 {
			id = "after"
		}
		return json.Marshal(map[string]any{"id": id, "env": []any{`FORGE_BROWSER_ENV={"KEY":"value"}`, nil}})
	})}
	r := Runtime{docker: &dk, browserValues: func(context.Context, ids.ID) (map[string]string, error) {
		return map[string]string{"KEY": "value"}, nil
	}}
	v := r.Probe(context.Background(), Target{ContractDir: dir, Container: "preview", Naked: server.URL}, "browser-env")
	if v.Outcome != check.CannotAssess || !v.Fault || !strings.Contains(v.Detail, "replaced") || calls != 2 {
		t.Fatalf("replacement passed: %+v (%d inspections)", v, calls)
	}
}

type dockerClientFunc func(context.Context, string, docker.Command) ([]byte, error)

func (f dockerClientFunc) Run(ctx context.Context, host string, c docker.Command) ([]byte, error) {
	return f(ctx, host, c)
}
