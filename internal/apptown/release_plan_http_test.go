package apptown

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/gitexec"
)

func TestReleasePlanHTTPProjectsActualHeadPublicationAndConfiguration(t *testing.T) {
	t.Parallel()
	a := authzFixture(t)
	h := a.Handler()
	if w := authzCall(h, "owner", "POST", "/api/v1/projects/game/repo", `{}`); w.Code != 200 {
		t.Fatalf("init: %d %s", w.Code, w.Body.String())
	}
	repo := a.projectRepoPath("game")
	head, err := gitexec.Run(t.Context(), repo, "rev-parse", "main")
	if err != nil {
		t.Fatal(err)
	}
	read := func() *httptest.ResponseRecorder {
		return authzCall(h, "owner", "GET", "/api/v1/projects/game/release/plan", "")
	}
	var plan struct {
		Configured                   bool
		Published                    *bool
		Deployment, URL, ConfigError string
		Head                         struct{ SHA, Subject string }
		ChangesSinceLast             []json.RawMessage
	}
	// Nothing published: the plan says so (the field is present, never
	// left out), and the head is main's.
	w := read()
	if err := json.Unmarshal(w.Body.Bytes(), &plan); err != nil || w.Code != 200 || plan.Configured || plan.ConfigError == "" || plan.Published == nil || *plan.Published || plan.Head.SHA != head || plan.ChangesSinceLast == nil || len(plan.ChangesSinceLast) != 0 {
		t.Fatalf("unconfigured plan: %d %s %v", w.Code, w.Body.String(), err)
	}
	// A bundle of image IDs is not published either.
	game, err := a.Projects.BySlug(t.Context(), "game")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Deployment.Bundles.Cut(t.Context(), game.ID, head, map[string]string{"web": "sha256:web"}, nil, []byte("contract"), deployment.BundleOrigin{}); err != nil {
		t.Fatal(err)
	}
	plan.Published = nil
	if w := read(); json.Unmarshal(w.Body.Bytes(), &plan) != nil || plan.Published == nil || *plan.Published {
		t.Fatalf("a plan over a bundle of image IDs: %d %s", w.Code, w.Body.String())
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/endpoints/3":
			fmt.Fprint(w, `{"Id":3,"Name":"lan","Type":1}`)
		case "/api/endpoints/3/docker/info":
			fmt.Fprint(w, `{"ID":"plan-engine"}`)
		default:
			t.Errorf("unexpected provider request %s", r.URL)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	if err := os.WriteFile(filepath.Join(a.cfg.dataDir, "secrets.env"), []byte("PORTAINER_URL="+server.URL+"\nPORTAINER_API_KEY=never-expose-this-key\nAPPTOWN_APPS_DOMAIN=apps.example\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if response := authzCall(h, "boss", "POST", "/api/v1/targets", `{"name":"intranet","kind":"portainer","endpointId":3,"environment":"lan","urlFrom":"PORTAINER_URL","apiKeyFrom":"PORTAINER_API_KEY","domain":"apps.example"}`); response.Code != 200 {
		t.Fatalf("verified attachment: %d %s", response.Code, response.Body)
	}
	w = read()
	plan.ConfigError = ""
	if err := json.Unmarshal(w.Body.Bytes(), &plan); err != nil || w.Code != 200 || !plan.Configured || plan.Deployment != "lan" || plan.URL != "" {
		t.Fatalf("configured plan: %d %s %v", w.Code, w.Body.String(), err)
	}
	if strings.Contains(w.Body.String(), "never-expose-this-key") {
		t.Fatal("plan leaked provider key")
	}
	target, err := a.Deployment.Targets.ByName(t.Context(), deployment.IntranetTargetName)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Deployment.Targets.Offer(t.Context(), target, "*", deployment.OfferCaps{}, "boss"); err != nil {
		t.Fatal(err)
	}
	dep, err := a.Deployment.Deployments.EnsureStanding(t.Context(), game.ID, "lan", target.ID, deployment.Attributes{Feed: "promotion", Audience: "organisation", DataClass: "non-production"})
	if err != nil {
		t.Fatal(err)
	}
	generated := "https://app-" + strings.ReplaceAll(dep.ID.String(), "-", "") + ".apps.example"
	for revision, domain := range []string{"game.example.org", ""} {
		path := "/api/v1/projects/game/deployments/" + dep.ID.String() + "/address"
		response := authzCall(h, "boss", http.MethodPut, path, fmt.Sprintf(`{"expectedRevision":%d,"customHostname":%q}`, revision, domain))
		if response.Code != http.StatusOK {
			t.Fatalf("address edit: %d %s", response.Code, response.Body)
		}
		want := generated
		if domain != "" {
			want = "https://game.example.org"
		}
		w = read()
		if err := json.Unmarshal(w.Body.Bytes(), &plan); err != nil || w.Code != 200 || plan.URL != want {
			t.Fatalf("plan for deployment address %q: %d %s %v", domain, w.Code, w.Body.String(), err)
		}
	}
	// Existing desired addresses survive target domain edits and restarts.
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
	if err := os.WriteFile(filepath.Join(a.cfg.dataDir, "secrets.env"), []byte("PORTAINER_URL="+server.URL+"\nPORTAINER_API_KEY=never-expose-this-key\nAPPTOWN_APPS_DOMAIN=other.example\n"), 0600); err != nil {
		t.Fatal(err)
	}
	a.SeedTargets(t.Context())
	w = read()
	if err := json.Unmarshal(w.Body.Bytes(), &plan); err != nil || !plan.Configured || plan.URL != generated {
		t.Fatalf("plan after a domain edit and a restart: %d %s %v", w.Code, w.Body.String(), err)
	}
	editDomain("")
	w = read()
	if err := json.Unmarshal(w.Body.Bytes(), &plan); err != nil || !plan.Configured || plan.URL != generated {
		t.Fatalf("plan with the domain cleared: %d %s %v", w.Code, w.Body.String(), err)
	}
	if w := authzCall(h, "owner", "GET", "/api/v1/projects/missing/release/plan", ""); w.Code != 404 {
		t.Fatalf("missing: %d %s", w.Code, w.Body.String())
	}
}
