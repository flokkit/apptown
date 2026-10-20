package forge

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flokkit.io/forge/internal/forge/deployment"
	"flokkit.io/forge/internal/gitexec"
)

func TestReleasePlanHTTPProjectsActualHeadContractAndConfiguration(t *testing.T) {
	t.Parallel()
	a := authzFixture(t)
	h := a.Handler()
	if w := authzCall(h, "owner", "POST", "/api/v1/projects/game/repo", `{}`); w.Code != 200 {
		t.Fatalf("init: %d %s", w.Code, w.Body.String())
	}
	repo := filepath.Join(a.cfg.dataDir, "workspaces", "project-game")
	head, err := gitexec.Run(t.Context(), repo, "rev-parse", "main")
	if err != nil {
		t.Fatal(err)
	}
	read := func() *httptest.ResponseRecorder {
		return authzCall(h, "owner", "GET", "/api/v1/projects/game/release/plan", "")
	}
	var plan struct {
		Configured                                            bool
		Deployment, Contract, URL, ConfigError, ContractError string
		Head                                                  struct{ SHA, Subject string }
		ChangesSinceLast                                      []json.RawMessage
	}
	w := read()
	if err := json.Unmarshal(w.Body.Bytes(), &plan); err != nil || w.Code != 200 || plan.Configured || plan.ConfigError == "" || plan.Contract != "none" || plan.Head.SHA != head || plan.ChangesSinceLast == nil || len(plan.ChangesSinceLast) != 0 {
		t.Fatalf("unconfigured plan: %d %s %v", w.Code, w.Body.String(), err)
	}
	if err := os.WriteFile(filepath.Join(a.cfg.dataDir, "secrets.env"), []byte("PORTAINER_URL=https://portainer.invalid\nPORTAINER_API_KEY=never-expose-this-key\nFORGE_APPS_DOMAIN=apps.example\n"), 0600); err != nil {
		t.Fatal(err)
	}
	a.SeedTargets(t.Context()) // a restart records the intranet target from the configuration
	dir := filepath.Join(repo, ".apptown")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ yaml, dockerfile, want string }{
		{"app:\n  web:\n    image: app:1\n    ports: [\"8080\"]\n    public: true\n", "", "image"},
		{"app:\n  web:\n    static: public\n    public: true\n", "", "from-source"},
		{"app:\n  web:\n    static: public\n    public: true\n", "FROM scratch\n", "static"},
		{"app: [broken", "", "invalid"},
	} {
		if err := os.WriteFile(filepath.Join(dir, "app.yaml"), []byte(tc.yaml), 0644); err != nil {
			t.Fatal(err)
		}
		if tc.dockerfile != "" {
			if err := os.WriteFile(filepath.Join(repo, "Dockerfile"), []byte(tc.dockerfile), 0644); err != nil {
				t.Fatal(err)
			}
		} else {
			_ = os.Remove(filepath.Join(repo, "Dockerfile"))
		}
		w = read()
		plan.ConfigError = ""
		if err := json.Unmarshal(w.Body.Bytes(), &plan); err != nil || w.Code != 200 || !plan.Configured || plan.Deployment != "lan" || plan.URL != "https://game.apps.example" || plan.Contract != tc.want {
			t.Fatalf("%s plan: %d %s %v", tc.want, w.Code, w.Body.String(), err)
		}
		if strings.Contains(w.Body.String(), "never-expose-this-key") {
			t.Fatal("plan leaked provider key")
		}
	}
	// The plan promises the intranet target's domain, as the release uses
	// it: an edited domain, kept when a restart reads configuration naming
	// another; a cleared one is unconfigured with the release's refusal.
	editDomain := func(domain string) {
		t.Helper()
		intranet, err := a.Deployment.Targets.ByName(t.Context(), deployment.IntranetTargetName)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.Deployment.Targets.EditSettings(context.Background(), intranet.Name, deployment.TargetSettingsEdit{ExpectedRevision: &intranet.SettingsRevision, Domain: &domain}, "admin"); err != nil {
			t.Fatal(err)
		}
	}
	editDomain("lan.example.org")
	if err := os.WriteFile(filepath.Join(a.cfg.dataDir, "secrets.env"), []byte("PORTAINER_URL=https://portainer.invalid\nPORTAINER_API_KEY=never-expose-this-key\nFORGE_APPS_DOMAIN=other.example\n"), 0600); err != nil {
		t.Fatal(err)
	}
	a.SeedTargets(t.Context())
	w = read()
	if err := json.Unmarshal(w.Body.Bytes(), &plan); err != nil || !plan.Configured || plan.URL != "https://game.lan.example.org" {
		t.Fatalf("plan after a domain edit and a restart: %d %s %v", w.Code, w.Body.String(), err)
	}
	editDomain("")
	plan.URL = ""
	w = read()
	if err := json.Unmarshal(w.Body.Bytes(), &plan); err != nil || plan.Configured || plan.URL != "" || !strings.Contains(plan.ConfigError, "names no domain") {
		t.Fatalf("plan with the domain cleared: %d %s %v", w.Code, w.Body.String(), err)
	}
	if w := authzCall(h, "owner", "GET", "/api/v1/projects/missing/release/plan", ""); w.Code != 404 {
		t.Fatalf("missing: %d %s", w.Code, w.Body.String())
	}
}
