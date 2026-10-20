package deployment

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/deploy/appcontract"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/deploy/portainer"
	"flokkit.io/forge/internal/forge/dependency"
	"flokkit.io/forge/internal/forge/integration"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/secret"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
)

func TestLANReleaseStagesSelectedCommitAfterBranchAdvances(t *testing.T) {
	t.Parallel()
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
	repo := filepath.Join(dir, "workspaces", "project-p")
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
	owner.runLANRelease(context.Background(), cut.ID, p, cfg, "alice", selected, cut.Number, "https://p.apps.example", "", nil)
	composeMu.Lock()
	defer composeMu.Unlock()
	if !strings.Contains(compose, "original") || strings.Contains(compose, "later") {
		var logs []string
		for _, e := range releaseEvents(t, db, cut.ID) {
			logs = append(logs, e.Text)
		}
		t.Fatalf("release staged a different revision: %s; log: %s", compose, strings.Join(logs, "\n"))
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

// Portainer runs a bundle's static image as the bundle records it: a
// self-serving image with no command, one of the earlier form with the
// file server's command, as it always ran.
func TestAnIntranetBundleRunsItsStaticImageAsItsRecordSays(t *testing.T) {
	t.Parallel()
	c, err := appcontract.Parse([]byte("app:\n  web:\n    public: true\n    static: site\n    ports: [\"8080\"]\n"))
	if err != nil {
		t.Fatal(err)
	}
	for _, self := range []bool{true, false} {
		b := &ReleaseBundle{ID: idstest.N(3), Parts: map[string]string{"web": "registry.example/p@sha256:" + strings.Repeat("a", 64)}, BuiltFrom: BundleOrigin{SelfServing: self}}
		plan, err := bundleApp(appcontract.Prepare(c, nil), stackImages{Parts: b.Parts, Init: b.Init, SelfServing: b.BuiltFrom.SelfServing})
		if err != nil {
			t.Fatal(err)
		}
		want := []string(nil)
		if !self {
			want = docker.StaticServerCommand(8080)
		}
		if web := plan.PublicPart(); !web.ServesStatic || web.Image != b.Parts["web"] || !slices.Equal(web.Command, want) {
			t.Fatalf("self-serving %v: %+v", self, web)
		}
	}
}
