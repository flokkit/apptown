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

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/identity"
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
	contract := "app:\n  web:\n    image: reg.example/app:1\n    ports: [\"8080\"]\n    public: true\n"
	if err := os.MkdirAll(filepath.Join(repo, ".apptown"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".apptown", "app.yaml"), []byte(contract), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := gitexec.Run(t.Context(), repo, "add", ".apptown/app.yaml"); err != nil {
		t.Fatal(err)
	}
	if _, err := gitexec.Run(t.Context(), repo, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "Selected runtime"); err != nil {
		t.Fatal(err)
	}
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
	if _, err := a.Deployment.Bundles.Cut(t.Context(), game.ID, head, map[string]string{"web": "sha256:web"}, nil, []byte(contract), deployment.BundleOrigin{}); err != nil {
		t.Fatal(err)
	}
	// Main can advance independently; both local and published bundles
	// still propose their own selected commit.
	if _, err := gitexec.Run(t.Context(), repo, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "Later main"); err != nil {
		t.Fatal(err)
	}
	plan.Published = nil
	if w := read(); json.Unmarshal(w.Body.Bytes(), &plan) != nil || plan.Published == nil || *plan.Published || plan.Head.SHA != head {
		t.Fatalf("a plan over a bundle of image IDs: %d %s", w.Code, w.Body.String())
	}
	local, err := a.Deployment.Targets.Put(t.Context(), &deployment.Target{Name: "release-local", Kind: deployment.KindDockerHost, Origin: deployment.OriginBuiltIn, Status: deployment.TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Deployment.Targets.Offer(t.Context(), local, "*", deployment.OfferCaps{}, "boss"); err != nil {
		t.Fatal(err)
	}
	localDep, err := a.Deployment.Deployments.EnsureStanding(t.Context(), game.ID, "office", local.ID, deployment.Attributes{Feed: "promotion", Audience: "team", DataClass: "synthetic"})
	if err != nil {
		t.Fatal(err)
	}
	selectDefault := func(id string, revision int64) {
		t.Helper()
		idJSON := "null"
		if id != "" {
			idJSON = fmt.Sprintf("%q", id)
		}
		response := authzCall(h, "boss", "PUT", "/api/v1/projects/game/release/default", fmt.Sprintf(`{"deploymentId":%s,"expectedRevision":%d}`, idJSON, revision))
		if response.Code != 200 {
			t.Fatalf("choose default: %d %s", response.Code, response.Body)
		}
	}
	selectDefault(localDep.ID.String(), 0)
	promotionGrant := testGrantID(t, a.DB, "group", identity.GroupAllUsers, access.RoleReleaseManager, "", access.ScopeTarget, "*")
	if promotionGrant.IsZero() || !revokeTestGrant(t, a, promotionGrant, "boss") {
		t.Fatal("remove inherited promotion permission")
	}
	if denied := authzCall(h, "outsider", "POST", "/api/v1/projects/game/release", `{"defaultRevision":1}`); denied.Code != 403 {
		t.Fatalf("default release without deploy permission: %d %s", denied.Code, denied.Body)
	}
	a.addGrant("user", "outsider", "", string(access.CapDeploymentDeploy), access.ScopeProject, game.ID.String(), "boss", "")
	response := authzCall(h, "outsider", "POST", "/api/v1/projects/game/release", `{"defaultRevision":1}`)
	var accepted struct{ Number int64 }
	if err := json.Unmarshal(response.Body.Bytes(), &accepted); err != nil || response.Code != 202 {
		t.Fatalf("deploy-only local default: %d %s %v", response.Code, response.Body, err)
	}
	recorded, err := a.Deployment.Releases.ByNumber(t.Context(), game.ID, accepted.Number)
	if err != nil || recorded.CommitSHA != head || recorded.DeploymentID != localDep.ID {
		t.Fatalf("displayed source differs from admitted release: %+v %v", recorded, err)
	}
	// Clearing the explicit default must not be undone by attaching Portainer.
	selectDefault("", 1)
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
	if err := json.Unmarshal(w.Body.Bytes(), &plan); err != nil || w.Code != 200 || plan.Configured || plan.ConfigError == "" || plan.URL != "" {
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
	selectDefault(dep.ID.String(), 2)
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
