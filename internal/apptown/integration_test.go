//go:build integration

package apptown

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/docker/dockertest"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/poll"
	"github.com/flokkit/apptown/internal/strs"
	"github.com/flokkit/apptown/stock"
)

// Integration tier (design/architecture/testing.md): these tests REQUIRE a running
// docker daemon and network access to pull small public images. They run
// real containers through the same code paths production uses — the boot
// check, the deploy, the reconciler, the vhost proxy. Run with:
//
//	go test -tags=integration ./internal/apptown/ -run TestIntegration -v
//
// Conventions: container names embed the test pid so parallel runs and a
// developer's live AppTown on the same daemon never collide; every
// container is removed in t.Cleanup; a missing daemon is a FAILURE, not
// a skip — a skip that always skips is how an integration tier rots.

func requireDocker(t *testing.T) {
	t.Helper()
	if err := dockertest.Run("info"); err != nil {
		t.Fatalf("integration tests require a running docker daemon (docker info failed: %v) — see design/architecture/testing.md", err)
	}
}

// staticFixture writes a repo-shaped workspace with a static runtime
// contract and a page that the static page check accepts.
func staticFixture(t *testing.T, dir, marker string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".apptown"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".apptown", "app.yaml"),
		[]byte("app:\n  web:\n    static: site\n    public: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "site"), 0o755); err != nil {
		t.Fatal(err)
	}
	page := fmt.Sprintf(`<!doctype html><html><body><h1>%s</h1><script>console.log("alive")</script></body></html>`, marker)
	if err := os.WriteFile(filepath.Join(dir, "site", "index.html"), []byte(page), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The preview boot: a change worktree with a static contract cold-boots in
// a real container, serves its page, reuses the live container on the
// second ask, and dies with reap.
func TestIntegrationStaticPreviewBoot(t *testing.T) {
	requireDocker(t)
	a := testApp(t)
	pid := plainTestProject(t, a.DB, "p", "P")

	chID := ids.New(time.Now()) // a random id: its container names collide with no other AppTown's
	// sql-guarantee: a bare change row for the preview; admission would start a run this test does not want.
	if _, err := a.DB.Exec(`INSERT INTO changes (id, number, project_id, type, title, state, branch, created_at) VALUES (?,1,?,'feature','t','in_progress','forge/x',?)`, chID, pid, now()); err != nil {
		t.Fatal(err)
	}
	ws := filepath.Join(a.cfg.dataDir, "workspaces", fmt.Sprintf("change-%d", chID))
	staticFixture(t, ws, "preview says hi")
	t.Cleanup(func() {
		docker.RemovePreview(context.Background(), a.docker, docker.PreviewChangeName(chID), docker.ChangeStackID(chID))
	})

	ch, _ := a.changeByID(context.Background(), chID)
	url, err := a.Preview.RehearseChange(context.Background(), previewChange(ch), false)
	if err != nil {
		t.Fatalf("cold boot: %v", err)
	}
	body := mustGET(t, url.URL)
	if !strings.Contains(body, "preview says hi") {
		t.Fatalf("preview serves the wrong content: %s", strs.Truncate(body, 200))
	}

	// Second ask reuses the live container (same URL, no re-boot): the
	// same address serving the same incarnation and public values. The
	// values are compared, not the pointer each answer carries them in.
	url2, err := a.Preview.RehearseChange(context.Background(), previewChange(ch), false)
	if err != nil || url2.URL != url.URL || url2.HostPort != url.HostPort || url2.Incarnation != url.Incarnation || !reflect.DeepEqual(url2.Public, url.Public) {
		t.Fatalf("reuse: %+v vs %+v (%v)", url2, url, err)
	}

	// Reap kills it dead.
	docker.RemovePreview(context.Background(), a.docker, docker.PreviewChangeName(chID), docker.ChangeStackID(chID))
	name := fmt.Sprintf("apptown-preview-change-%d", chID)
	if dockertest.Run("inspect", name) == nil {
		t.Fatal("reap left the container")
	}
}

// A change whose contract declares sidecar services previews like any
// other — the snapshot boots its own throwaway service stack (blank, a look
// not a restore), the app comes up, and reap tears down the pvsnap network
// with it. Guards the fix for the "this app needs sidecar services" refusal
// that used to turn service-backed apps away from the change preview.
func TestIntegrationServiceBackedPreviewBoots(t *testing.T) {
	requireDocker(t)
	a := testApp(t)
	a.SyncStockDependencies(context.Background()) // the app.yaml contract declares redis as a need
	pid := plainTestProject(t, a.DB, "p", "P")
	chID := ids.New(time.Now()) // a random id: its container names collide with no other AppTown's
	// sql-guarantee: a bare change row for the preview; admission would start a run this test does not want.
	if _, err := a.DB.Exec(`INSERT INTO changes (id, number, project_id, type, title, state, branch, created_at) VALUES (?,1,?,'feature','t','in_progress','forge/x',?)`, chID, pid, now()); err != nil {
		t.Fatal(err)
	}
	ws := filepath.Join(a.cfg.dataDir, "workspaces", fmt.Sprintf("change-%d", chID))
	// A static app is enough to prove the wiring: no refusal, the sidecar
	// network is created, the app serves, and reap clears the network. A
	// redis sidecar stays up on its default command with no required env.
	if err := os.MkdirAll(filepath.Join(ws, "site"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.MkdirAll(filepath.Join(ws, ".apptown"), 0o755)
	_ = os.WriteFile(filepath.Join(ws, ".apptown", "app.yaml"), []byte(`
app:
  site:
    static: site
    public: true
needs:
  redis: {}
`), 0o644)
	_ = os.WriteFile(filepath.Join(ws, "site", "index.html"),
		[]byte(`<!doctype html><html><body><h1>service-backed preview</h1></body></html>`), 0o644)
	network := fmt.Sprintf("apptown-net-pvsnap-change-%d", chID)
	t.Cleanup(func() {
		docker.RemovePreview(context.Background(), a.docker, docker.PreviewChangeName(chID), docker.ChangeStackID(chID))
	})

	ch, _ := a.changeByID(context.Background(), chID)
	url, err := a.Preview.RehearseChange(context.Background(), previewChange(ch), false)
	if err != nil {
		t.Fatalf("service-backed preview must boot, not refuse: %v", err)
	}
	if !strings.Contains(mustGET(t, url.URL), "service-backed preview") {
		t.Fatal("preview served the wrong content")
	}
	if dockertest.Run("network", "inspect", network) != nil {
		t.Fatalf("the snapshot's sidecar network %s was not created", network)
	}
	docker.RemovePreview(context.Background(), a.docker, docker.PreviewChangeName(chID), docker.ChangeStackID(chID))
	if dockertest.Run("network", "inspect", network) == nil {
		t.Fatal("reap left the sidecar network behind")
	}
}

// The contract, end to end: a `.apptown/app.yaml` with a build: public part and
// a need boots a preview — AppTown builds the repo Dockerfile, runs it, and
// stands the sidecar network up beside it. Proves the one contract form maps
// onto the runtime for a from-source app with a dependency.
func TestIntegrationBuildContractBoots(t *testing.T) {
	requireDocker(t)
	a := testApp(t)
	a.SyncStockDependencies(context.Background()) // the app.yaml contract declares redis as a need
	pid := plainTestProject(t, a.DB, "p", "P")
	chID := ids.New(time.Now()) // a random id: its container names collide with no other AppTown's
	// sql-guarantee: a bare change row for the preview; admission would start a run this test does not want.
	if _, err := a.DB.Exec(`INSERT INTO changes (id, number, project_id, type, title, state, branch, created_at) VALUES (?,1,?,'feature','t','in_progress','forge/x',?)`, chID, pid, now()); err != nil {
		t.Fatal(err)
	}
	ws := filepath.Join(a.cfg.dataDir, "workspaces", fmt.Sprintf("change-%d", chID))
	_ = os.MkdirAll(filepath.Join(ws, ".apptown"), 0o755)
	_ = os.WriteFile(filepath.Join(ws, "Dockerfile"),
		[]byte("FROM "+stock.Images().DF+"\nRUN echo 'build contract lives' > /index.html\nEXPOSE 8080\nCMD [\"httpd\",\"-f\",\"-p\",\"8080\",\"-h\",\"/\"]\n"), 0o644)
	_ = os.WriteFile(filepath.Join(ws, ".apptown", "app.yaml"), []byte(`
app:
  web:
    build: .
    ports: ["8080"]
    public: true
needs:
  redis: {}
`), 0o644)
	network := fmt.Sprintf("apptown-net-pvsnap-change-%d", chID)
	t.Cleanup(func() {
		docker.RemovePreview(context.Background(), a.docker, docker.PreviewChangeName(chID), docker.ChangeStackID(chID))
	})

	ch, _ := a.changeByID(context.Background(), chID)
	url, err := a.Preview.RehearseChange(context.Background(), previewChange(ch), false)
	if err != nil {
		t.Fatalf("build-contract preview must boot: %v", err)
	}
	if !strings.Contains(mustGET(t, url.URL), "build contract lives") {
		t.Fatal("the built app served the wrong content")
	}
	if dockertest.Run("network", "inspect", network) != nil {
		t.Fatalf("the sidecar network %s was not created", network)
	}
}

// The standing deployment, live: RunDeploy boots the app cold from the
// project repo, records the environment; killing the container and running
// the boot reconciler brings it back on the same port; the vhost router
// proxies a tier hostname to it end-to-end.
func TestIntegrationDeployReconcileAndVHost(t *testing.T) {
	requireDocker(t)
	a := testApp(t, Config{TierDomains: []string{"main=main.itest.example"}})
	a.SeedTargets(t.Context())
	slug := fmt.Sprintf("itest-%d", os.Getpid())
	plainTestProject(t, a.DB, slug, "IT")
	p, _ := a.Projects.BySlug(context.Background(), slug)
	if err := a.lifecycle.InitializeRepository(t.Context(), &p, "", ""); err != nil {
		t.Fatal(err)
	}
	recordFixtureAdoptions(t, a)
	repo := a.projectRepoPath(slug)
	staticFixture(t, repo, "deployed and standing")
	stageAll(repo)
	if _, err := gitexec.Run(t.Context(), repo, "commit", "-m", "app"); err != nil {
		t.Fatal(err)
	}
	defineWorkflows(t, a, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "itest-deploy", Version: 1}, Spec: schema.Spec{
		Kind: "pipeline", Role: "landing", Stages: []schema.Stage{{Name: "boot", Steps: []schema.Step{{Name: "deploy", Uses: "deploy"}}}},
	}})
	a.Work.Stop()
	ctx := a.asAppTown(context.Background(), p.ID, "integration")
	change, err := a.Work.Create(ctx, workflow.ChangeDraft{ProjectID: p.ID, Type: "feature", Title: "deploy"})
	if err != nil {
		t.Fatal(err)
	}
	// The lane deploys with no approval before it, so its start needs a
	// person who may land (workflow/assent.go), not AppTown itself.
	run, err := a.Work.StartRun(asLander(t, a), workflow.StartChangeRun{ChangeID: change.ID, Workflow: "itest-deploy"})
	if !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatal(err)
	}
	snapshot, err := a.Work.Snapshot(ctx, run.ID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	var stepID ids.ID
	for _, candidate := range snapshot.Steps {
		if candidate.Name == "deploy" {
			stepID = candidate.ID
			break
		}
	}
	if stepID.IsZero() {
		t.Fatal("deploy step was not materialized")
	}
	if err := a.Work.ExecuteStep(a.asEngine(context.Background()), stepID); err != nil {
		t.Fatal(err)
	}
	selected, err := a.Work.Step(a.asEngine(context.Background()), stepID)
	if err != nil {
		t.Fatal(err)
	}
	step := selected.Step
	if step.Status != workflow.StatusPass {
		t.Fatalf("deploy step: %q %s", step.Status, step.Evidence)
	}
	environment, err := a.Deployment.Deployments.ByName(ctx, p.ID, "main")
	if err != nil {
		t.Fatal("no environment row recorded")
	}
	// The app's container is named by the deployment, as AppTown names it.
	names, err := environment.HostNames()
	if err != nil {
		t.Fatal(err)
	}
	envContainer := names.App()
	t.Cleanup(func() { _ = dockertest.Run("rm", "-f", envContainer) })
	hostPort := environment.HostPort
	if !strings.Contains(mustGET(t, fmt.Sprintf("http://127.0.0.1:%d/", hostPort)), "deployed and standing") {
		t.Fatal("environment does not serve the app")
	}

	// Kill it; the reconciler stands it back up on the SAME port.
	// The checkout has advanced, but rebuild must keep the selected source.
	staticFixture(t, repo, "a later revision that must not be deployed")
	stageAll(repo)
	if _, err := gitexec.Run(ctx, repo, "commit", "-m", "advance checkout after deployment"); err != nil {
		t.Fatal(err)
	}
	// docker rm -f succeeds on a name that is not there, so the kill first
	// sees the container it kills.
	if err := dockertest.Run("inspect", envContainer); err != nil {
		t.Fatalf("the deployed container %s is not there to kill: %v", envContainer, err)
	}
	if err := dockertest.Run("rm", "-f", envContainer); err != nil {
		t.Fatal(err)
	}
	a.ensureDeploymentsOnce(ctx)
	// Lose completion persistence after boot; the rebuild is not replayed.
	dbtest.FailNextStatement(t, a.DB, "UPDATE deployments SET observed_generation")
	job := runQueuedJobForTest(t, a, "rebuild", slug+"/main")
	if job.State != "discarded" {
		t.Fatalf("rebuild interruption: %s", job.State)
	}
	var attemptID ids.ID
	for _, attempt := range openAttempts(t, a) {
		if attemptStatus(t, a, attempt.ID) == deployment.AttemptUncertain {
			if !attemptID.IsZero() {
				t.Fatal("more than one uncertain attempt")
			}
			attemptID = attempt.ID
		}
	}
	if attemptID.IsZero() {
		t.Fatal("the interrupted rebuild left no uncertain attempt")
	}
	result, err := a.Deployment.Recoverer().Run(context.Background(), attemptID)
	if err != nil || result.Outcome != "recovered" {
		t.Fatalf("recover real Docker generation: %+v %v", result, err)
	}
	environment, err = a.Deployment.Deployments.ByName(ctx, p.ID, "main")
	if err != nil || environment.ObservedGeneration != 2 {
		t.Fatalf("recovered generation: %+v %v", environment, err)
	}
	if !strings.Contains(mustGET(t, fmt.Sprintf("http://127.0.0.1:%d/", hostPort)), "deployed and standing") {
		t.Fatal("reconciler did not stand the environment back up")
	}

	// The vhost router proxies <slug>.main.<domain> to it, end-to-end.

	h := a.Handler()
	req := httptest.NewRequest("GET", "/", nil)
	req.Host = slug + ".main.itest.example"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "deployed and standing") {
		t.Fatalf("vhost proxy: %d %s", rec.Code, strs.Truncate(rec.Body.String(), 200))
	}
}

func mustGET(t *testing.T, url string) string {
	t.Helper()
	var lastErr error
	var body string
	if poll.Until(context.Background(), 20*time.Second, 100*time.Millisecond, func() bool {
		res, err := http.Get(url)
		if err != nil {
			lastErr = err
			return false
		}
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != 200 {
			lastErr = fmt.Errorf("status %d", res.StatusCode)
			return false
		}
		body = string(b)
		return true
	}) {
		return body
	}
	t.Fatalf("GET %s never came good: %v", url, lastErr)
	return ""
}
