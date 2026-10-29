//go:build integration

package apptown

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/datalayout"
	"github.com/flokkit/apptown/internal/apptown/dataprotection"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/instancebackup"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/docker/dockertest"
	"github.com/flokkit/apptown/internal/dockerlabel"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/poll"
	"github.com/flokkit/apptown/internal/stockupdate/stockupdatetest"
	"github.com/flokkit/apptown/internal/strs"
	"github.com/flokkit/apptown/internal/testinputs"
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
	for _, explicit := range []bool{false, true} {
		t.Run(fmt.Sprint(explicit), func(t *testing.T) {
			a := testApp(t)
			pid := plainTestProject(t, a.DB, "p", "P")

			chID := ids.New(time.Now()) // a random id: its container names collide with no other AppTown's
			// sql-guarantee: a bare change row for the preview; admission would start a run this test does not want.
			if _, err := a.DB.Exec(`INSERT INTO changes (id, number, project_id, type, title, state, branch, created_at) VALUES (?,1,?,'feature','t','in_progress','forge/x',?)`, chID, pid, now()); err != nil {
				t.Fatal(err)
			}
			ws := filepath.Join(a.cfg.dataDir, "workspaces", fmt.Sprintf("change-%d", chID))
			staticFixture(t, ws, "preview says hi")
			if explicit {
				if err := os.WriteFile(filepath.Join(ws, ".apptown", "app.yaml"), []byte("app:\n  web:\n    static: site\n    public: true\n    health:\n      readiness: {http: /, expect: 200, period: 5s, timeout: 2s, successThreshold: 2, failureThreshold: 2, checksDeps: false}\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
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
		})
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
	job := runQueuedJobForTest(t, a, "rebuild", p.ID.String()+"/main")
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

// This is a successful archive/load/startup composition check, restored into
// the same path and into another one.
// Nonparallel: the real built-in database uses its documented fixed port and
// Restore must see a host without any other AppTown installation.
func TestIntegrationRestoredStartupCompletesRelocationAndIntent(t *testing.T) {
	requireDocker(t)
	dk := docker.Scope{Client: docker.CLI{}}
	for _, key := range []string{dockerlabel.Instance, dockerlabel.Deployment} {
		names, err := dk.List(t.Context(), "ps", "-a", "--filter", "label="+key, "--format", "{{.Names}}")
		if err != nil || len(names) != 0 {
			t.Fatalf("restore qualification requires a host with no other AppTown containers (%s): %v %v", key, names, err)
		}
	}
	probe, err := net.Listen("tcp", "127.0.0.1:"+builtinDBPort)
	if err != nil {
		t.Fatalf("restore qualification needs the free built-in database port: %v", err)
	}
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "apptownd")
	testinputs.GoBuild(t, "", []string{"GOOS=linux", "CGO_ENABLED=0"}, "../../cmd/apptownd")
	if out := (execution.Runner{}).Run(t.Context(), execution.Local, execution.Request{Argv: []string{"go", "build", "-o", binary, "../../cmd/apptownd"}, Env: []string{"GOOS=linux", "CGO_ENABLED=0"}, Timeout: 3 * time.Minute}); !out.Exited0() {
		t.Fatalf("build restore daemon: %v %s", out.RunErr, out.Output)
	}
	shipped := stockupdatetest.Dir(t)
	for _, changedRoot := range []bool{false, true} {
		t.Run(fmt.Sprintf("changedRoot=%t", changedRoot), func(t *testing.T) {
			restoredStartupCase(t, dk, binary, shipped, changedRoot)
		})
	}
}

func restoredStartupCase(t *testing.T, dk docker.Scope, binary, shipped string, changedRoot bool) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	root := t.TempDir()
	source := filepath.Join(root, "source")
	target := source
	if changedRoot {
		target = filepath.Join(root, "restored")
	}
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	suffix := ids.New(time.Now()).String()
	names := builtinDBNames{Container: "apptown-restore-test-" + suffix, Volume: "apptown-restore-test-data-" + suffix, Database: "apptown"}
	// Register only exact test-owned resources before their first creation.
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := dk.Remove(cleanup, names.Container); err != nil {
			t.Error(err)
		}
		if err := dk.RemoveVolume(cleanup, names.Volume); err != nil {
			t.Error(err)
		}
	})
	url := "postgres://postgres:restore-test-only@127.0.0.1:" + builtinDBPort + "/apptown?sslmode=disable"
	bootstrap := "APPTOWN_DB_URL=" + url + "\nAPPTOWN_DB_PASSWORD=restore-test-only\nAPPTOWN_DB_CONTAINER=" + names.Container + "\nAPPTOWN_DB_VOLUME=" + names.Volume + "\n"
	if err := os.WriteFile(filepath.Join(source, "db.env"), []byte(bootstrap), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "layout"), []byte(fmt.Sprintf("%d\n", DataLayoutVersion)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (BuiltinDatabase{DataDir: source, Docker: dk}).Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	db, err := OpenURL(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	app := startApp(t, db, Config{DataDir: source, DockerClient: docker.CLI{}, UsingBuiltinDB: true})
	app.SeedTargets(ctx)
	testAccounts(t, db, "restore-fixture")
	pid, err := database.Transact(ctx, db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(ctx, tx, time.Now(), project.Spec{Slug: "restore-project"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Projects.SetDefaultBranch(ctx, pid, "main"); err != nil {
		t.Fatal(err)
	}
	hub := filepath.Join(source, "workspaces", "project-"+pid.String())
	if err := os.MkdirAll(hub, 0o700); err != nil {
		t.Fatal(err)
	}
	git := func(folder string, args ...string) string {
		t.Helper()
		out, err := gitexec.HubRun(ctx, folder, args...)
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return out
	}
	git(hub, "init", "--quiet", "--template=", "-b", "main")
	git(hub, "config", "user.name", gitexec.FolderUserName)
	git(hub, "config", "user.email", gitexec.FolderUserEmail)
	git(hub, "remote", "add", "origin", "https://example.invalid/unchanged-project.git")
	if err := os.WriteFile(filepath.Join(hub, "tracked"), []byte("main content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(hub, "add", "tracked")
	git(hub, "commit", "--quiet", "-m", "installed main")
	mainSHA := git(hub, "rev-parse", "HEAD")
	if err := scm.RecordMain(ctx, db, hub, pid, "main", scm.ViaInit, time.Now()); err != nil {
		t.Fatal(err)
	}
	created, err := database.Transact(ctx, db, func(tx *database.Tx) (workflow.CreatedChange, error) {
		return workflow.CreateChangeIn(ctx, tx, workflow.ChangeDraft{ProjectID: pid, Title: "preserved local work"}, "restore-fixture", time.Now())
	})
	if err != nil {
		t.Fatal(err)
	}
	workspace := (workflow.Layout{DataDir: source}).ChangeWorkspace(created.Change.ID)
	if _, err := (workflow.GitWorkspace{DataDir: source}).EnsureClone(ctx, workflow.CloneRequest{Repository: hub, Workspace: workspace, Branch: created.Change.Branch, DefaultBranch: "main"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "tracked"), []byte("staged content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(workspace, "add", "tracked")
	if err := os.WriteFile(filepath.Join(workspace, "tracked"), []byte("dirty content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "untracked"), []byte("untracked content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	index, dirty := git(workspace, "diff", "--cached", "--binary"), git(workspace, "diff", "--binary")
	cloud, err := app.Deployment.Targets.Put(ctx, &deployment.Target{Name: "restore-cloud", Kind: deployment.KindDOApp, Status: deployment.TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	remote, err := app.Deployment.Deployments.EnsureStanding(ctx, pid, "cloud", cloud.ID, deployment.Attributes{}.WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	addressTarget, err := app.Deployment.Targets.Put(ctx, &deployment.Target{Name: "restore-address", Kind: deployment.KindPortainer, Status: deployment.TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	addressDep, err := app.Deployment.Deployments.EnsureStanding(ctx, pid, "address", addressTarget.ID, deployment.Attributes{}.WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	addressSnapshot := deployment.AddressSnapshot{DeploymentID: addressDep.ID, TargetID: addressTarget.ID, Revision: 4, Ingress: deploy.Ingress{Hosts: []string{"legacy.apps.example", "customer.example"}, URL: "https://customer.example", CertificateDomain: "apps.example"}}
	addressJSON, err := json.Marshal(addressSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: this archive fixture retains an address revision the owner would not produce in one step; no live provider observation is claimed.
	if _, err := db.Exec(`INSERT INTO deployment_address_settings(deployment_id,generated_hostname,custom_hostname,certificate_domain,revision) VALUES($1,'legacy.apps.example','customer.example','apps.example',4)`, addressDep.ID); err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"legacy.apps.example", "customer.example", "previous.example"} {
		// sql-guarantee: include an old retained claim absent from current desired ingress, to prove dump/restore does not recompute reservations.
		if _, err := db.Exec(`INSERT INTO deployment_hostname_claims(hostname) VALUES($1)`, host); err != nil {
			t.Fatal(err)
		}
		// sql-guarantee: the membership is retained archive state, not a new hostname acquisition.
		if _, err := db.Exec(`INSERT INTO deployment_hostname_claimants(hostname,deployment_id) VALUES($1,$2)`, host, addressDep.ID); err != nil {
			t.Fatal(err)
		}
	}
	addressRelease, err := app.Deployment.Releases.Start(ctx, deployment.ReleaseDraft{Source: deployment.ReleaseFromBranch, ProjectID: pid, DeploymentID: addressDep.ID, Deployment: addressDep.Name, Revision: mainSHA, Actor: "restore-fixture"}, "retained address archive fixture")
	if err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: an already-admitted historical request must retain its exact immutable ingress across the archive boundary.
	if _, err := db.Exec(`INSERT INTO release_requests(release_id,target_id,base_generation,attributes_json,ingress_json,require_promotion) VALUES($1,$2,0,'{}',$3,true)`, addressRelease.ID, addressTarget.ID, addressJSON); err != nil {
		t.Fatal(err)
	}
	addressAttempt, _, err := app.Deployment.Outcomes.Admit(ctx, deployment.Start{DeploymentID: addressDep.ID, TargetID: addressTarget.ID, Key: "address-archive", Inputs: deployment.Inputs{ReleaseID: addressRelease.ID, Revision: mainSHA, ArtifactHash: "retained-address-artifact", Address: &addressSnapshot}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.Deployment.Outcomes.Complete(ctx, deployment.Completion{Attempt: addressAttempt, Observed: deploy.Observation{Generation: addressAttempt.Generation, Rollout: "active", URL: addressSnapshot.Ingress.URL, Handle: map[string]any{"stack": "retained-address-stack"}}}); err != nil {
		t.Fatal(err)
	}
	addressFacts := func(db *database.DB) string {
		t.Helper()
		var facts string
		// sql-guarantee: archive identity covers raw legacy evidence and every retained claim, not only public desired-address projection.
		if err := db.SQL().QueryRowContext(ctx, `SELECT jsonb_build_object('settings',(SELECT to_jsonb(s) FROM deployment_address_settings s WHERE deployment_id=$1),'claims',(SELECT jsonb_agg(hostname ORDER BY hostname) FROM deployment_hostname_claimants WHERE deployment_id=$1),'request',(SELECT jsonb_build_object('ingress',ingress_json,'requirePromotion',require_promotion,'authority',authority_json) FROM release_requests WHERE release_id=$2),'releaseDefault',(SELECT to_jsonb(r) FROM project_release_settings r WHERE project_id=$4),'attempt',(SELECT ingress_json FROM deployment_attempt_inputs WHERE attempt_id=$3))::text`, addressDep.ID, addressRelease.ID, addressAttempt.ID, pid).Scan(&facts); err != nil {
			t.Fatal(err)
		}
		return facts
	}
	retainedAddressFacts := addressFacts(db)

	// Admit real consent through its owner before archiving. The fixed procedure
	// collaborator isolates restore provenance from workflow selection (covered
	// separately); identity, grant checks, revision storage and archive are real.
	if _, err := app.Access.Grant(ctx, identity.Spec{SubjectType: "user", Subject: "restore-fixture", Role: access.RoleInstanceAdmin, Scope: access.ScopeInstance, Origin: "fixture", By: "restore-fixture"}, ""); err != nil {
		t.Fatal(err)
	}
	consentCtx := access.WithCaller(ctx, access.Caller{Verified: true, Principal: access.Principal{ID: testPrincipalOf(t, db, "restore-fixture"), Kind: "user", Name: "restore-fixture", Via: "session"}})
	consentOwner := workflow.NewStandingRules(db, workflow.StandingDependencies{Authority: access.Checked{Authority: app.Access, Projects: app.Projects}, Identity: app.Access, Procedures: restoreStandingProcedure{}})
	consentInput := workflow.StandingDefinition{Projects: []workflow.StandingProject{{ProjectID: pid, Workflow: "restore-fixture"}}, Categories: workflow.StandingCategories{Dependencies: "patch"}, Delivery: "land-only"}
	question, err := consentOwner.Preview(consentCtx, consentInput)
	if err != nil {
		t.Fatal(err)
	}
	consent, err := consentOwner.Enable(consentCtx, workflow.StandingCommand{Definition: consentInput, ShownDigest: question.Digest})
	if err != nil {
		t.Fatal(err)
	}
	refuseRestoreWrite := !changedRoot
	if refuseRestoreWrite {
		// sql-guarantee: test-only archive fault injection. The constraint allows
		// existing history to restore but refuses the daemon's disabled revision.
		if _, err := db.Exec(`ALTER TABLE standing_rule_revisions ADD CONSTRAINT test_restore_standing_write CHECK (revision = 1)`); err != nil {
			t.Fatal(err)
		}
	}
	backupPath := filepath.Join(source, "backups", "restore-project", "main", "recorded-set")
	if err := os.MkdirAll(backupPath, 0o700); err != nil {
		t.Fatal(err)
	}
	setID, err := dataprotection.NewBackups(db).Record(ctx, &dataprotection.Backup{ProjectID: pid, Deployment: "main", TakenAt: time.Now().UTC().Format(time.RFC3339Nano), Trigger: dataprotection.TriggerPreRelease, Path: backupPath, Status: dataprotection.BackupOK, Store: dataprotection.StoreAppTownDisk}, "restore-fixture")
	if err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(root, "source.backup")
	const passphrase = "disposable-restore-qualification-passphrase"
	report, err := app.Backups.Backup(ctx, instancebackup.Request{Archive: archive, Passphrase: passphrase, Actor: "restore-fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if report.Manifest.DataDir != source || report.Manifest.Database == nil {
		t.Fatal("source archive lacks actual root or database provenance")
	}
	if err := app.ShutdownBackground(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := dk.Remove(ctx, names.Container); err != nil {
		t.Fatal(err)
	}
	if err := dk.RemoveVolume(ctx, names.Volume); err != nil {
		t.Fatal(err)
	}
	retired := filepath.Join(root, "retired-source")
	if err := os.Rename(source, retired); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	passFile := filepath.Join(root, "passphrase")
	if err := os.WriteFile(passFile, []byte(passphrase), 0o600); err != nil {
		t.Fatal(err)
	}
	out := (execution.Runner{}).Run(ctx, execution.Local, execution.Request{Argv: []string{binary, "-data", target, "-shipped", shipped, "restore", archive}, Env: []string{"APPTOWN_BACKUP_PASSPHRASE_FILE=" + passFile}, Timeout: 3 * time.Minute})
	if refuseRestoreWrite {
		if out.Exited0() || !strings.Contains(string(out.Output), "disable restored standing approvals") {
			t.Fatalf("restore write failure was not a startup refusal: %+v", out)
		}
		if pending, err := instancebackup.ReadPendingRestore(target); err != nil || pending == nil {
			t.Fatalf("failed disabling lost loaded continuation: %+v %v", pending, err)
		}
		repair, err := OpenURL(ctx, url)
		if err != nil {
			t.Fatal(err)
		}
		// sql-guarantee: remove only this test's fault after observing the real
		// daemon refusal, allowing its loaded-intent restart to retry normally.
		_, repairErr := repair.Exec(`ALTER TABLE standing_rule_revisions DROP CONSTRAINT test_restore_standing_write`)
		closeErr := repair.Close()
		if repairErr != nil || closeErr != nil {
			t.Fatalf("remove injected fault: %v %v", repairErr, closeErr)
		}
		out = (execution.Runner{}).Run(ctx, execution.Local, execution.Request{Argv: []string{binary, "-data", target, "-shipped", shipped}, Timeout: 3 * time.Minute})
	}
	if !out.Exited0() {
		t.Fatalf("restored daemon: %v (exit %d)\n%s", out.RunErr, out.ExitCode, out.Output)
	}
	if !strings.Contains(string(out.Output), "restored AppTown") {
		t.Fatalf("daemon did not finish restore: %s", out.Output)
	}
	if pending, err := instancebackup.ReadPendingRestore(target); err != nil || pending != nil {
		t.Fatalf("restore continuation not completed: %+v %v", pending, err)
	}
	if err := datalayout.RequireCurrent(target); err != nil {
		t.Fatal("relocation was not finalized", err)
	}
	restored, err := OpenURL(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := restored.Close(); err != nil {
			t.Error(err)
		}
	}()
	owner, err := project.New(restored, nil).ByID(ctx, pid)
	if err != nil || owner.Slug != "restore-project" || owner.ServingLabel != "restore-project" {
		t.Fatalf("project rows lost: %+v %v", owner, err)
	}

	restoredIdentity := identity.New(restored, identity.Config{Projects: project.NewCatalogue(restored)})
	restoredConsent := workflow.NewStandingRules(restored, workflow.StandingDependencies{Authority: access.Checked{Authority: restoredIdentity, Projects: project.New(restored, nil)}, Identity: restoredIdentity})
	history, err := restoredConsent.History(consentCtx, consent.RuleID)
	if err != nil || len(history) != 2 || !history[0].Enabled || history[1].Enabled || history[1].ChangedBy.Name != "restore" || history[1].Issuer.Principal != consent.Issuer.Principal || history[0].Question.Shown != history[1].Question.Shown {
		t.Fatalf("restored standing consent not disabled with provenance preserved: %+v %v", history, err)
	}
	main, found, err := scm.ReadMain(ctx, restored, pid)
	if err != nil || !found || main.SHA != mainSHA {
		t.Fatalf("main receipt changed: %+v %v %v", main, found, err)
	}
	currentHub := filepath.Join(target, "workspaces", "project-"+pid.String())
	if git(currentHub, "rev-parse", "HEAD") != mainSHA || git(currentHub, "remote", "get-url", "origin") != "https://example.invalid/unchanged-project.git" {
		t.Fatal("restored hub identity changed")
	}
	currentWorkspace := (workflow.Layout{DataDir: target}).ChangeWorkspace(created.Change.ID)
	if git(currentWorkspace, "diff", "--cached", "--binary") != index || git(currentWorkspace, "diff", "--binary") != dirty {
		t.Fatal("restored staged or dirty work changed")
	}
	raw, err := os.ReadFile(filepath.Join(currentWorkspace, "untracked"))
	if err != nil || string(raw) != "untracked content\n" {
		t.Fatalf("untracked content lost: %q %v", raw, err)
	}
	set, err := dataprotection.NewBackups(restored).ByID(ctx, pid, "main", setID)
	if err != nil || set.Path != filepath.Join(target, "backups", "restore-project", "main", "recorded-set") {
		t.Fatalf("backup reference not relocated: %+v %v", set, err)
	}
	deploymentRow, err := deployment.NewDeployments(restored).ByID(ctx, remote.ID)
	if err != nil || deploymentRow.RestoreCheck != deployment.RestoreUnconfirmed {
		t.Fatalf("external restore hold missing: %+v %v", deploymentRow, err)
	}
	if got := addressFacts(restored); got != retainedAddressFacts {
		t.Fatalf("archive changed address ownership, evidence, claims or admitted snapshots: before %s after %s", retainedAddressFacts, got)
	}
	addressHeld, err := deployment.NewDeployments(restored).ByID(ctx, addressDep.ID)
	if err != nil || addressHeld.RestoreCheck != deployment.RestoreUnconfirmed {
		t.Fatalf("restored address owner lost activation hold: %+v %v", addressHeld, err)
	}
	receipts, err := audit.Recent(ctx, restored, audit.Filter{KindPrefix: "instance.restored"})
	if err != nil || len(receipts) != 1 {
		t.Fatalf("restore receipt missing/repeated: %d %v", len(receipts), err)
	}
	if changedRoot {
		if _, err := os.Stat(source); !os.IsNotExist(err) {
			t.Fatalf("restore reused original root: %v", err)
		}
	}
	if _, err := os.Stat(filepath.Join(retired, "workspaces", "project-"+pid.String(), "tracked")); err != nil {
		t.Fatal("retired source evidence lost", err)
	}
}

// The restore test supplies declarations only; the real owner still requires
// verified current human authority and writes immutable consent itself.
type restoreStandingProcedure struct{}

func (restoreStandingProcedure) StandingProcedure(context.Context, database.Queryer, ids.ID, string) (workflow.StandingProcedure, error) {
	return workflow.StandingProcedure{Acceptance: workflow.StandingPurpose{Workflow: "restore-fixture", Version: 1, Step: "accept", Means: "accept bounded updates after required checks"}, Landing: workflow.StandingPurpose{Workflow: "land", Version: 1, Step: "accept", Means: "land the checked update"}}, nil
}
