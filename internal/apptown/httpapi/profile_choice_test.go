package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/ids"
)

type listedDeployments []deployment.Summary

func (l listedDeployments) List(context.Context, ids.ID) ([]deployment.Summary, error) { return l, nil }

// An app without a secretspec.toml chooses a deployment's profile from
// app.yaml, on any standing deployment and on previews, and returns to
// default; a name neither file declares is refused.
func TestADeploymentsProfileIsChosenFromEitherFileAndReturnsToDefault(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	dir := t.TempDir()
	secrets := secret.NewService(db, secret.Config{DataDir: dir})
	projects := project.New(db, nil)
	projectID := httpTestProject(t, db, "p", "Project")
	h := NewSecrets(secrets, integration.NewService(db, integration.Dependencies{Secrets: secrets}), projects,
		listedDeployments{{Name: "cloud", Kind: deployment.Standing}, {Name: "copy", Kind: deployment.Restored}}, db, dir, nil)
	repo := filepath.Join(dir, "workspaces", "project-"+projectID.String(), ".apptown")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	app := "app:\n  web: {image: app:1, public: true}\nprofiles:\n  production:\n    web: {resources: {memory: 2G}}\n  preview:\n    web: {resources: {memory: 256M}}\n"
	if err := os.WriteFile(filepath.Join(repo, "app.yaml"), []byte(app), 0o644); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /api/v1/projects/{slug}/secrets/{deployment}", h.SetSecretProfile)
	mux.HandleFunc("GET /api/v1/projects/{slug}/secrets", h.ProjectSecrets)
	choose := func(dep, profile string) int {
		req := httptest.NewRequest(http.MethodPut, "/api/v1/projects/p/secrets/"+dep, strings.NewReader(`{"profile":"`+profile+`"}`))
		req = req.WithContext(access.WithCaller(req.Context(), access.Caller{Principal: access.Principal{Kind: "user", Name: "bls", Via: "session"}, Verified: true, Receipt: "bls"}))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code
	}
	p, err := projects.BySlug(t.Context(), "p")
	if err != nil {
		t.Fatal(err)
	}
	profileOf := func(dep string) string {
		name, _, err := secrets.DeploymentProfile(t.Context(), p.ID, dep, nil, mustContract(t, app))
		if err != nil {
			t.Fatal(err)
		}
		return name
	}
	for _, c := range []struct {
		dep, profile string
		code         int
		then         string
	}{
		{"cloud", "production", 200, "production"},
		{"cloud", "default", 200, "default"},
		{"preview", "default", 200, "default"}, // an explicit default beats the preview profile
		{"preview", "production", 200, "production"},
		{"cloud", "staging", 422, ""},
		{"copy", "production", 404, ""}, // a restored copy is not a standing deployment
	} {
		if got := choose(c.dep, c.profile); got != c.code {
			t.Errorf("choosing %s for %s: %d, want %d", c.profile, c.dep, got, c.code)
		}
		if c.then != "" && profileOf(c.dep) != c.then {
			t.Errorf("after choosing %s for %s it uses %s", c.profile, c.dep, profileOf(c.dep))
		}
	}
	// The page reads what may be chosen and what each deployment uses,
	// though the app declares no secrets.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/p/secrets", nil)
	req = req.WithContext(access.WithCaller(req.Context(), access.Caller{Principal: access.Principal{Kind: "user", Name: "bls", Via: "session"}, Verified: true, Receipt: "bls"}))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	var read ProjectSecrets
	if err := json.Unmarshal(rec.Body.Bytes(), &read); err != nil || rec.Code != 200 {
		t.Fatalf("GET: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Join(read.Choosable, ",") != "default,preview,production" {
		t.Errorf("choosable: %v", read.Choosable)
	}
	chosen := map[string]string{}
	for _, d := range read.Deployments {
		chosen[d.Name] = d.Chosen
	}
	if chosen["preview"] != "production" || chosen["cloud"] != "default" || chosen["main"] != "default" {
		t.Errorf("each deployment's profile as read: %v", chosen)
	}
}

func mustContract(t *testing.T, yaml string) *appcontract.Contract {
	t.Helper()
	c, err := appcontract.Parse([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	return c
}
