package integration

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/secret"
)

// The intranet's apps domain is the APPTOWN_APPS_DOMAIN secret: the stock
// seed names it, so a boot's seed sync moves the managed binding to it as a
// new version.
func TestTheIntranetAppsDomainHasItsAppTownName(t *testing.T) {
	t.Parallel()
	var seeded string
	for _, s := range Seeds(SeedConfig{}) {
		if s.Name == "portainer-deploy" {
			seeded = s.Config
		}
	}
	if !strings.Contains(seeded, `"appsDomainFrom":"APPTOWN_APPS_DOMAIN"`) {
		t.Fatalf("the stock intranet seed names %s", seeded)
	}

	db := testDB(t)
	dir := t.TempDir()
	s := NewService(db, Dependencies{Projects: project.New(db, nil), Secrets: secret.NewService(db, secret.Config{DataDir: dir})})
	write := func(env string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "secrets.env"), []byte(env), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("PORTAINER_URL=https://portainer.invalid\nPORTAINER_API_KEY=k\nAPPTOWN_APPS_DOMAIN=apps.example\n")
	if cfg, err := s.PortainerTarget(t.Context()); err != nil || cfg.AppsDomain != "apps.example" {
		t.Fatalf("the AppTown-named apps domain: %+v %v", cfg, err)
	}
}

// An error-reporting organisation with no team gets one AppTown creates, named
// for AppTown; an existing team is used as it is.
func TestTheErrorReportingTeamAppTownCreatesIsNamedForIt(t *testing.T) {
	t.Parallel()
	var created string
	teams := "[]"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			var body struct{ Slug string }
			_ = json.NewDecoder(r.Body).Decode(&body)
			created = body.Slug
			w.WriteHeader(201)
			return
		}
		_, _ = w.Write([]byte(teams))
	}))
	t.Cleanup(srv.Close)
	s := NewService(nil, Dependencies{})
	if team, err := s.errorsTeam(&errorsBinding{URL: srv.URL, Org: "o"}, "tok"); err != nil || team != "apptown" || created != "apptown" {
		t.Fatalf("created %q, used %q: %v", created, team, err)
	}
	teams, created = `[{"slug":"ours"}]`, ""
	if team, err := s.errorsTeam(&errorsBinding{URL: srv.URL, Org: "o"}, "tok"); err != nil || team != "ours" || created != "" {
		t.Fatalf("an existing team: used %q, created %q: %v", team, created, err)
	}
}
