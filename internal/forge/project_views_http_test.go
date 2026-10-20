package forge

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flokkit.io/forge/internal/forge/deployment"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/gitexec"
	"flokkit.io/forge/internal/gitexec/gittest"
	"flokkit.io/forge/internal/ids/idstest"
)

func TestProjectHTTPSettingsRejectBeforeWritingAndReturnSavedMetadata(t *testing.T) {
	t.Parallel()
	a := authzFixture(t)
	h := a.Handler()
	path := "/api/v1/projects/game"
	for _, tc := range []struct {
		who, body, code string
		status          int
	}{
		{"owner", `{"expectedRevision":0,"name":"Unauthorized"}`, "", 403}, {"boss", `{`, "", 400},
		{"boss", `{"expectedRevision":0,"slug":"INVALID","icon":"changed","blurb":"changed"}`, "", 400},
		{"boss", `{"expectedRevision":0,"slug":"open","icon":"changed","namespace":"changed"}`, "", 409},
		// An edit must say which reading it started from, and that reading must be current.
		{"boss", `{"icon":"changed"}`, "", 400},
		{"boss", `{"expectedRevision":7,"icon":"changed"}`, "settings-moved", 409},
	} {
		w := authzCall(h, tc.who, "PATCH", path, tc.body)
		if w.Code != tc.status || w.Header().Get("X-AppTown-Deny-Code") != tc.code {
			t.Fatalf("settings refusal %s: %d %q %s", tc.body, w.Code, w.Header().Get("X-AppTown-Deny-Code"), w.Body.String())
		}
		p, err := a.Projects.BySlug(context.Background(), "game")
		if err != nil || p.Name != "G" || p.Icon != "" || p.Blurb != "" || p.Namespace != "" {
			t.Fatalf("rejected settings persisted: %+v %v", p, err)
		}
	}
	w := authzCall(h, "boss", "PATCH", path, `{"expectedRevision":0,"name":"Game revised","namespace":"family","icon":"star","blurb":"our game"}`)
	var p project.Project
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil || w.Code != 200 || p.ID != idstest.N(1) || p.Name != "Game revised" || p.Namespace != "family" || p.Icon != "star" || p.Blurb != "our game" || p.SettingsRevision != 1 {
		t.Fatalf("saved metadata response: %d %s %v", w.Code, w.Body.String(), err)
	}
	w = authzCall(h, "owner", "GET", path, "")
	var read project.Project
	if err := json.Unmarshal(w.Body.Bytes(), &read); err != nil || w.Code != 200 || read.Namespace != p.Namespace || read.Name != p.Name || read.Icon != p.Icon {
		t.Fatalf("metadata read: %d %s %v", w.Code, w.Body.String(), err)
	}
}

func TestRepositoryHTTPDiffAndManifestIdentifyCurrentProjectWork(t *testing.T) {
	t.Parallel()
	a := workAuthorityFixture(t, Config{DockerClient: newFakeDocker()})
	rebuildWork(t, a, func(d *workflow.Dependencies) { d.Git = workflow.GitWorkspace{DataDir: a.cfg.dataDir} })
	h := a.Handler()
	base := "/api/v1/projects/game"
	// Invalid input must not silently initialize a default repository.
	for _, body := range []string{`{`, `{"defaultBranch":false}`} {
		w := authzCall(h, "owner", "POST", base+"/repo", body)
		if w.Code != 400 {
			t.Fatalf("repo malformed: %d %s", w.Code, w.Body.String())
		}
		p, err := a.Projects.BySlug(context.Background(), "game")
		if err != nil || p.DefaultBranch != "" {
			t.Fatalf("malformed init persisted: %+v %v", p, err)
		}
	}
	if w := authzCall(h, "owner", "POST", base+"/repo", `{}`); w.Code != 200 {
		t.Fatalf("init: %d %s", w.Code, w.Body.String())
	}
	repo := a.projectRepoPath("game")
	// The project already has a Change, so its toolchain arrives as a landing
	// would leave it: committed on main. The toolchain door is for new projects.
	if err := os.WriteFile(filepath.Join(repo, "mise.toml"), []byte("[tools]\ngo = \"1.26\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "mise.toml"}, {"-c", "user.name=t", "-c", "user.email=t@localhost", "commit", "-qm", "toolchain"}} {
		if out, err := gitexec.Run(t.Context(), repo, args...); err != nil {
			t.Fatalf("%v: %s", args, out)
		}
	}
	seedLibraryWorkflow(t, a, idstest.N(1), "review", `apiVersion: apptown.dev/v1
kind: Workflow
metadata: {name: review}
spec:
  kind: lifecycle
  stages:
    - name: review
      steps:
        - {name: accept, uses: approval, with: {means: "I checked it"}}
`)
	w := authzCall(h, "owner", "GET", base+"/manifest", "")
	var manifest struct {
		Project struct{ Slug string }
		Repo    struct {
			Backed        bool
			DefaultBranch string
		}
		Workflows []struct {
			Name    string
			Version int
		}
		Packs, Guidance, Policies []json.RawMessage
		Bindings                  []json.RawMessage
	}
	if err := json.Unmarshal(w.Body.Bytes(), &manifest); err != nil || w.Code != 200 || manifest.Project.Slug != "game" || !manifest.Repo.Backed || manifest.Repo.DefaultBranch != "main" || len(manifest.Workflows) != 1 || manifest.Workflows[0].Name != "review" || manifest.Workflows[0].Version != 1 || manifest.Packs == nil || manifest.Guidance == nil || manifest.Policies == nil || manifest.Bindings == nil {
		t.Fatalf("manifest: %d %s %v", w.Code, w.Body.String(), err)
	}
	ch, err := a.Work.Create(asPerson("owner"), workflow.ChangeDraft{ProjectID: idstest.N(1), Title: "One diff"})
	if err != nil {
		t.Fatal(err)
	}
	ws := filepath.Join(a.cfg.dataDir, "workspaces", fmt.Sprintf("change-%d", ch.ID))
	if err := os.WriteFile(filepath.Join(ws, "contract-test.txt"), []byte("only this change\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "contract-test.txt"}, {"commit", "-qm", "work"}} {
		if _, err := gitexec.Run(t.Context(), ws, args...); err != nil {
			t.Fatal(err)
		}
	}
	run, err := a.Work.StartRun(asPerson("owner"), workflow.StartChangeRun{ChangeID: ch.ID, Workflow: "implement"})
	if err != nil {
		t.Fatal(err)
	}
	a.Work.Wait()

	// Run comparison begins at admission; the change comparison also includes
	// commits made in its chat before this run began.
	runPath := fmt.Sprintf("/api/v1/runs/%d/diff", run.ID)
	w = authzCall(h, "owner", "GET", runPath, "")
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != "" {
		t.Fatalf("new run inherited prior diff: %d %s", w.Code, w.Body.String())
	}
	if err := os.WriteFile(filepath.Join(ws, "during-run.txt"), []byte("during this run\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "during-run.txt"}, {"commit", "-qm", "run work"}} {
		if _, err := gitexec.Run(t.Context(), ws, args...); err != nil {
			t.Fatal(err)
		}
	}
	refs := gittest.Run(t, repo, "show-ref")
	for _, tc := range []struct {
		path  string
		prior bool
	}{{fmt.Sprintf("/api/v1/changes/%d/diff", ch.ID), true}, {runPath, false}} {
		if w := authzCall(h, "outsider", "GET", tc.path, ""); w.Code != 403 {
			t.Fatalf("private diff: %d %s", w.Code, w.Body.String())
		}
		w := authzCall(h, "owner", "GET", tc.path, "")
		if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") || !strings.Contains(w.Body.String(), "+during this run") || strings.Contains(w.Body.String(), "+only this change") != tc.prior {
			t.Fatalf("diff %s: %d %s", tc.path, w.Code, w.Body.String())
		}
	}

	detailPath := fmt.Sprintf("/api/v1/changes/%d", ch.ID)
	head := strings.TrimSpace(gittest.Run(t, ws, "rev-parse", "HEAD"))
	checkDetail := func() {
		t.Helper()
		w := authzCall(h, "owner", "GET", detailPath, "")
		var detail struct {
			CommittedDiff struct{ Status string }
			Commits       []struct{ SHA, Subject string }
			Diffstat      string
			Head          string
		}
		if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil || w.Code != 200 || detail.CommittedDiff.Status != "changed" || len(detail.Commits) != 2 || detail.Commits[0].Subject != "run work" || !strings.Contains(detail.Diffstat, "2 files changed") {
			t.Fatalf("committed detail: %d %s %v", w.Code, w.Body.String(), err)
		}
		// The change's current commit, which a preview or a screenshot is
		// compared with, is the newest commit the page lists, in full.
		if detail.Head != head || !strings.HasPrefix(head, detail.Commits[0].SHA) {
			t.Fatalf("head %q, the change is at %q (newest listed %q)", detail.Head, head, detail.Commits[0].SHA)
		}
	}
	checkDetail()
	if got := gittest.Run(t, repo, "show-ref"); got != refs {
		t.Fatalf("GET published project refs: before=%s after=%s", refs, got)
	}

	filePath := fmt.Sprintf("/api/v1/runs/%d/workspace/during-run.txt", run.ID)
	if w := authzCall(h, "outsider", "GET", filePath, ""); w.Code != 403 {
		t.Fatalf("private workspace: %d %s", w.Code, w.Body.String())
	}
	w = authzCall(h, "owner", "GET", filePath, "")
	if w.Code != 200 || w.Body.String() != "during this run\n" {
		t.Fatalf("workspace content: %d %s", w.Code, w.Body.String())
	}
	// A broken workspace must not silently read the project's older branch.
	if err := os.Rename(filepath.Join(ws, ".git"), filepath.Join(ws, "git-backup")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{fmt.Sprintf("/api/v1/changes/%d/diff", ch.ID), runPath} {
		w := authzCall(h, "owner", "GET", path, "")
		if w.Code != 500 || !strings.Contains(w.Body.String(), "comparison workspace") {
			t.Fatalf("broken workspace: %d %s", w.Code, w.Body.String())
		}
	}
	w = authzCall(h, "owner", "GET", detailPath, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"unavailable"`) || !strings.Contains(w.Body.String(), "comparison workspace") {
		t.Fatalf("broken comparison detail: %d %s", w.Code, w.Body.String())
	}
	if err := os.Rename(filepath.Join(ws, "git-backup"), filepath.Join(ws, ".git")); err != nil {
		t.Fatal(err)
	}
	// Publication is an explicit writer operation; readers can use it once
	// the workspace is gone, retaining both the run and change fork points.
	if err := mirrorBranchForTest(repo, ws, ch.Branch); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(ws); err != nil {
		t.Fatal(err)
	}
	refs = gittest.Run(t, repo, "show-ref")
	checkDetail()
	for _, tc := range []struct {
		path  string
		prior bool
	}{{fmt.Sprintf("/api/v1/changes/%d/diff", ch.ID), true}, {runPath, false}} {
		w := authzCall(h, "owner", "GET", tc.path, "")
		if w.Code != 200 || !strings.Contains(w.Body.String(), "+during this run") || strings.Contains(w.Body.String(), "+only this change") != tc.prior {
			t.Fatalf("published comparison: %d %s", w.Code, w.Body.String())
		}
	}
	if got := gittest.Run(t, repo, "show-ref"); got != refs {
		t.Fatalf("GET changed published refs: %s", got)
	}
	w = authzCall(h, "boss", "GET", "/api/v1/projects", "")
	var projects []struct {
		Slug        string
		ActiveItems int
		Toolchain   []string
	}
	if err := json.Unmarshal(w.Body.Bytes(), &projects); err != nil || w.Code != 200 {
		t.Fatalf("projects: %d %s %v", w.Code, w.Body.String(), err)
	}
	found := false
	for _, p := range projects {
		if p.Slug == "game" {
			found = true
			if p.ActiveItems != 2 || len(p.Toolchain) == 0 || p.Toolchain[0] != "go" {
				t.Fatalf("project activity: %+v", p)
			}
		}
	}
	if !found {
		t.Fatal("game absent from projects")
	}
}

// The projects list tells a project that was never deployed from one whose
// deployment nobody has observed yet: both have no observation, but only the
// second is "status unknown".
func TestProjectListSaysWhetherAProjectHasADeployment(t *testing.T) {
	t.Parallel()
	a := authzFixture(t)
	h := a.Handler()
	deployed := func() map[string]bool {
		t.Helper()
		w := authzCall(h, "boss", "GET", "/api/v1/projects", "")
		var rows []struct {
			Slug       string `json:"slug"`
			Deployed   bool   `json:"deployed"`
			ObservedAt string `json:"observedAt"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil || w.Code != 200 {
			t.Fatalf("project list: %d %s %v", w.Code, w.Body.String(), err)
		}
		out := map[string]bool{}
		for _, r := range rows {
			out[r.Slug] = r.Deployed
		}
		return out
	}
	if got := deployed(); got["game"] {
		t.Fatalf("a project with no deployment was listed as deployed: %v", got)
	}
	p, err := a.Projects.BySlug(context.Background(), "game")
	if err != nil {
		t.Fatal(err)
	}
	target, err := a.Deployment.Targets.Put(context.Background(), &deployment.Target{Name: "list-host", Kind: deployment.KindDockerHost, Origin: deployment.OriginBuiltIn, Status: deployment.TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Deployment.Deployments.EnsureStanding(t.Context(), p.ID, "main", target.ID, (deployment.Attributes{}).WithDefaults()); err != nil {
		t.Fatal(err)
	}
	if got := deployed(); !got["game"] {
		t.Fatalf("a project with a main deployment was listed as not deployed: %v", got)
	}
}
