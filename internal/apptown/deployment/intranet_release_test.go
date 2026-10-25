package deployment

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/dependency"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/deploy/portainer"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
	"github.com/flokkit/apptown/internal/trust/trusttest"
)

// The stack also gives the app the extra certificates for elsewhere (the
// company's, never this machine's local authority).
// Not parallel: it installs the process's trust.
func TestLANReleaseStagesSelectedCommitAfterBranchAdvances(t *testing.T) {
	company, local := trusttest.New(t, "Company CA"), trusttest.New(t, "Local CA")
	trusttest.Install(t, company.Bundle(t), local.Bundle(t))
	db := testDB(t)
	dir := t.TempDir()
	pid, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "p", Name: "P"})
	})
	if err != nil {
		t.Fatal(err)
	}
	projects := project.New(db, nil)
	if err := projects.SetDefaultBranch(context.Background(), pid, "main"); err != nil {
		t.Fatal(err)
	}
	rec, err := project.ByIDIn(context.Background(), db, pid)
	if err != nil {
		t.Fatal(err)
	}
	p := &rec
	secrets := secret.NewService(db, secret.Config{DataDir: dir})
	integrations := integration.NewService(db, integration.Dependencies{Projects: projects, Secrets: secrets})
	owner := New(context.Background(), db, assembled(db, testRuntime{DataDir: dir, Projects: projects, Secrets: secrets, Integrations: integrations, Needs: dependency.NewCatalog(db, nil)}, Dependencies{}))
	repo := filepath.Join(dir, "workspaces", "project-"+pid.String())
	if err := os.MkdirAll(repo, 0755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-b", "main"}, {"config", "user.name", "Test"}, {"config", "user.email", "test@example.test"}} {
		if _, err := releaseGit(t.Context(), repo, args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(repo, ".apptown"), 0o755); err != nil {
		t.Fatal(err)
	}
	commit := func(marker string) string {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, ".apptown", "app.yaml"), []byte("app:\n  web:\n    image: example/app:1\n    ports: ['8080']\n    public: true\n    environment:\n      MARKER: "+marker+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := releaseGit(t.Context(), repo, "add", "-A"); err != nil {
			t.Fatal(err)
		}
		if _, err := releaseGit(t.Context(), repo, "commit", "-m", marker); err != nil {
			t.Fatal(err)
		}
		rev, err := (deploy.SelectedSource{Repository: repo, Revision: "HEAD"}).Resolve(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return rev
	}
	selected := commit("original")
	commit("later")
	var composeMu sync.Mutex
	var compose string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/endpoints":
			fmt.Fprint(w, `[{"Id":3,"Name":"selected"}]`)
		case "/api/endpoints/3":
			fmt.Fprint(w, `{"Id":3,"Name":"selected","Type":1}`)
		case "/api/endpoints/3/docker/info":
			fmt.Fprint(w, `{"ID":"selected-commit-engine"}`)
		case "/api/endpoints/3/docker/version":
			fmt.Fprint(w, `{"Os":"linux","Arch":"amd64"}`)
		case "/api/stacks":
			fmt.Fprint(w, `[]`)
		case "/api/stacks/create/standalone/string":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			composeMu.Lock()
			compose, _ = body["stackFileContent"].(string)
			composeMu.Unlock()
			fmt.Fprint(w, `{}`)
		default:
			t.Errorf("unexpected Portainer request: %s", r.URL)
			http.Error(w, "unexpected", 500)
		}
	}))
	defer server.Close()
	cfg := &portainer.Target{PortainerURL: server.URL, EnvName: "selected", AppsDomain: "apps.example", ReleaseDeployment: "lan"}
	// This internal execution test supplies an observed destination snapshot;
	// public owner attachment/admission is qualified separately.
	scope, err := portainer.ObserveScope(t.Context(), *cfg, 3)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Scope = &scope

	if _, err := owner.Targets.Put(context.Background(), &Target{Name: IntranetTargetName, Kind: KindPortainer, Provider: "portainer-deploy", Origin: OriginAttached, Status: TargetReady}); err != nil {
		t.Fatal(err)
	}
	dep, err := owner.intranetDeployment(t.Context(), p.ID, cfg)
	if err != nil {
		t.Fatal(err)
	}
	cut, err := owner.Releases.Start(context.Background(), ReleaseDraft{Source: ReleaseFromBranch, ProjectID: p.ID, DeploymentID: dep.ID, Deployment: "lan", Revision: selected, Actor: "alice"}, "selected revision fixture")
	if err != nil {
		t.Fatal(err)
	}
	if cut.Number != 1 {
		t.Fatalf("first release number: %d", cut.Number)
	}
	bundle := publishedBundleFixture(t, db, p.ID, selected, map[string]string{"web": "registry.example/team/app@sha256:" + strings.Repeat("a", 64)})
	owner.runLANRelease(context.Background(), cut.ID, p, cfg, "alice", selected, cut.Number, "https://p.apps.example", "", bundle)
	composeMu.Lock()
	defer composeMu.Unlock()
	if !strings.Contains(compose, "original") || strings.Contains(compose, "later") {
		var logs []string
		for _, e := range releaseEvents(t, db, cut.ID) {
			logs = append(logs, e.Text)
		}
		t.Fatalf("release staged a different revision: %s; log: %s", compose, strings.Join(logs, "\n"))
	}
	if !strings.Contains(compose, "APPTOWN_TRUST") || !strings.Contains(compose, strings.Split(string(company.PEM()), "\n")[1]) || strings.Contains(compose, strings.Split(string(local.PEM()), "\n")[1]) {
		t.Fatalf("the stack does not give the app the company's certificate alone:\n%s", compose)
	}
	if r := storedRelease(t, db, p.ID, cut.ID); r.State != Released || r.CommitSHA != selected {
		t.Fatalf("LAN release: %s at %q, want released at %q", r.State, r.CommitSHA, selected)
	}
	// Every attempt on the instance, with the inputs it was admitted with.
	ran := 0
	for _, a := range attemptsIn(t, db) {
		in, err := NewOutcomes(db, nil).InputsFor(context.Background(), Attempt{ID: a.ID, DeploymentID: a.DeploymentID, Generation: a.Generation})
		if err != nil {
			t.Fatal(err)
		}
		if in.ReleaseID == cut.ID && in.Revision == selected {
			ran++
		}
	}
	wantCount(t, "attempts admitted with the selected revision", ran, 1)
}

// Portainer runs a bundle's static image with no command of its own: the
// image carries the command that serves it.
func TestAnIntranetBundleRunsItsStaticImageAsItsRecordSays(t *testing.T) {
	t.Parallel()
	c, err := appcontract.Parse([]byte("app:\n  web:\n    public: true\n    static: site\n    ports: [\"8080\"]\n"))
	if err != nil {
		t.Fatal(err)
	}
	b := &ReleaseBundle{ID: idstest.N(3), Parts: map[string]string{"web": "registry.example/p@sha256:" + strings.Repeat("a", 64)}}
	plan, err := bundleApp(appcontract.Prepare(c, nil), stackImages{Parts: b.Parts, Init: b.Init})
	if err != nil {
		t.Fatal(err)
	}
	if web := plan.PublicPart(); !web.ServesStatic || web.Image != b.Parts["web"] || len(web.Command) != 0 {
		t.Fatalf("the static part: %+v", web)
	}
}
