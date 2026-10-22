package apptown

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/audit"
)

// Exercise the assembled authentication, route binding, lifecycle command and
// repository owner through an actual loopback HTTP server.
func TestRepositorySetupHTTPReceipt(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	testAdministrator(t, a, "bls")
	server := httptest.NewServer(a.Handler())
	defer server.Close()
	client := &http.Client{Timeout: 10 * time.Second}
	call := func(actor, method, path, body string, want int) string {
		t.Helper()
		req, err := http.NewRequest(method, server.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-AppTown-Actor", actor)
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != want {
			t.Fatalf("%s %s: %d %s", method, path, resp.StatusCode, data)
		}
		return string(data)
	}
	call("bls", "POST", "/api/v1/projects", `{"slug":"repository-smoke"}`, 201)
	path := "/api/v1/projects/repository-smoke/repo"
	call("outsider", "POST", path, `{"defaultBranch":"unwanted"}`, 403)
	if body := call("bls", "POST", path, `{"defaultBranch":"primary"}`, 200); !strings.Contains(body, `"defaultBranch":"primary"`) {
		t.Fatalf("repository response: %s", body)
	}
	call("bls", "POST", path, `{}`, 409)
	call("bls", "GET", "/api/v1/projects/repository-smoke", "", 200)
	if p, err := a.Projects.BySlug(context.Background(), "repository-smoke"); err != nil || p.DefaultBranch != "primary" {
		t.Fatalf("initialized project: %+v %v", p, err)
	}
	if n := auditsWhere(t, a.DB, "project.repo.initialized", func(e audit.Entry) bool { return e.Actor == "bls" && e.Subject == "repository-smoke" }); n != 1 {
		t.Fatalf("initialization receipts: %d", n)
	}
}

// An AppTown with no GitLab connection says so plainly: no groups, configured
// false, and a success, so the new-project wizard offers no GitLab copy
// instead of showing a failure (and asking again) on every visit.
func TestSCMNamespacesWithoutGitLabSayNotConfigured(t *testing.T) {
	t.Parallel()
	a := authzFixture(t)
	w := authzCall(a.Handler(), "boss", "GET", "/api/v1/scm/namespaces", "")
	var got struct {
		Namespaces []any `json:"namespaces"`
		Configured *bool `json:"configured"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != 200 || got.Configured == nil || *got.Configured || got.Namespaces == nil || len(got.Namespaces) != 0 {
		t.Fatalf("namespaces without GitLab: %d %s %v", w.Code, w.Body.String(), err)
	}
}
