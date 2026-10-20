package forge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"encoding/base64"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/console"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/forge/verification"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
	"flokkit.io/forge/internal/worker"
)

func runtimeGate(t *testing.T, place gatePlace, beforeBoot, afterBoot func(*App, ids.ID, string, *fakePreviewWorker)) (*workflow.Step, string, string) {
	t.Helper()
	const given = `{"GLITCHTIP_DSN":"https://key@errors.example/3","SENTRY_DSN":"https://key@errors.example/3"}`
	const loads = `<html><head><script src="/_apptown/env.js"></script><script src="game.js"></script></head></html>`
	page := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/_apptown/env.js" {
			w.Write([]byte(docker.BrowserEnvScript(given)))
			return
		}
		w.Write([]byte(loads))
	})
	server := httptest.NewServer(page)
	t.Cleanup(server.Close)
	daemon := newFakeDocker()
	daemon.port = server.Listener.Addr().(*net.TCPAddr).Port
	cfg := Config{DockerClient: daemon, DockerProbe: &docker.Probe{Settle: time.Millisecond}}
	var fake *fakePreviewWorker
	if place.onWorker {
		var values map[string]string
		_ = json.Unmarshal([]byte(given), &values)
		fake = &fakePreviewWorker{app: page, answer: func(spec worker.PreviewSpec) worker.PreviewState {
			return worker.PreviewState{Running: true, Incarnation: spec.Incarnation, ContainerID: "c1", BrowserValues: values}
		}}
		cfg.TierDomains = []string{"preview=preview.example.com"}
		cfg = withPreviewWorker(t, fake, cfg)
	}
	app := testApp(t, cfg)
	app.SyncStockDependencies(context.Background()) // error reporting's definition says what the page is given
	stages := []schema.Stage{{Name: "wire", Steps: []schema.Step{{Name: "boot", Uses: "preview"}, {Name: "guidance-conformance", Uses: "guidance/conformance"}}}}
	a, ctx, ch := changeScenarioIn(t, app, schema.Spec{Kind: "pipeline", Role: "working", Stages: stages})
	adopt(t, a, ch.ProjectID, "deploy/browser-values", 1, `[{"name":"page-loads-browser-values","expects":"loads the public values","evaluator":"runtime","probe":"browser-env","severity":"high"}]`)
	rebuildWork(t, a, func(deps *workflow.Dependencies) {
		deps.Automatic = []string{"guidance/conformance", "preview"}
		deps.Runners = a.workDependencies().Runners
		deps.Report = a.workDependencies().Report
	})
	a.Work.Stop()
	run, err := a.Work.StartRun(ctx, workflow.StartChangeRun{ChangeID: ch.ID, Workflow: "test"})
	if !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatal(err)
	}
	if err := a.Integrations.Attachments.Remember(ctx, ch.ProjectID, "errors", "glitchtip", `{"dsn":"https://key@errors.example/3"}`); err != nil {
		t.Fatal(err)
	}
	source, err := workflow.ReadSourceIn(context.Background(), a.DB, workflow.RunRef{ID: run.ID, ProjectID: ch.ProjectID})
	if err != nil {
		t.Fatal(err)
	}
	ws := source.Workspace(a.cfg.dataDir)
	if err := os.MkdirAll(filepath.Join(ws, ".apptown"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, ".apptown", "app.yaml"), []byte("app:\n  web: {static: ., public: true}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	commitFolder(t, ws)
	snap, err := a.Work.Snapshot(ctx, run.ID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	var gate, boot ids.ID
	for _, st := range snap.Steps {
		switch st.Name {
		case "guidance-conformance":
			gate = st.ID
		case "boot":
			boot = st.ID
		}
	}
	if beforeBoot != nil {
		beforeBoot(a, ch.ProjectID, ws, fake)
	}
	if err := a.Work.ExecuteStep(a.asEngine(context.Background()), boot); err != nil {
		t.Fatal(err)
	}
	if booted, err := workflow.ReadStepIn(context.Background(), a.DB, boot); err != nil || booted.Status != workflow.StatusPass {
		if err != nil {
			t.Fatal(err)
		}
		return booted, "", ""
	}
	if place.booted != nil {
		booted, err := workflow.ReadStepIn(context.Background(), a.DB, boot)
		if err != nil {
			t.Fatal(err)
		}
		place.booted(booted)
	}
	name := docker.PreviewRunName(run.ID)
	if !place.onWorker {
		daemon.markStartedWithBrowser(name, given)
	}
	if place.afterBoot != nil {
		place.afterBoot(t, ws, daemon, fake)
	}
	if afterBoot != nil {
		afterBoot(a, ch.ProjectID, ws, fake)
	}
	if err := a.Work.ExecuteStep(a.asEngine(context.Background()), gate); err != nil {
		t.Fatal(err)
	}
	saved, err := workflow.ReadStepIn(context.Background(), a.DB, gate)
	if err != nil {
		t.Fatal(err)
	}
	var evidence struct{ Checks verification.Results }
	if err := json.Unmarshal(saved.Evidence, &evidence); err != nil {
		t.Fatal(err)
	}
	for _, r := range evidence.Checks {
		if r.Check == "page-loads-browser-values" {
			return saved, r.Verdict.String(), r.Detail
		}
	}
	t.Fatalf("the check was not judged: %s", saved.Evidence)
	return nil, "", ""
}

// A worker transport failure belongs to the execution environment, not the app.
func TestAnUnavailableWorkerIsNotTheAppsFailure(t *testing.T) {
	t.Parallel()
	step, _, _ := runtimeGate(t, gatePlace{onWorker: true}, func(_ *App, _ ids.ID, _ string, f *fakePreviewWorker) { f.setDown(true) }, nil)
	if step.Status != workflow.StatusError {
		t.Fatalf("worker outage became %s: %s", step.Status, step.Evidence)
	}
}

// Attachment edits after boot must not change the values this incarnation is judged against.
func TestRuntimeChecksJudgeTheValuesThePreviewBootedWith(t *testing.T) {
	t.Parallel()
	for _, onWorker := range []bool{false, true} {
		t.Run(fmt.Sprint(onWorker), func(t *testing.T) {
			_, verdict, detail := runtimeGate(t, gatePlace{onWorker: onWorker}, nil, func(a *App, id ids.ID, _ string, _ *fakePreviewWorker) {
				if err := a.Integrations.Attachments.Remember(t.Context(), id, "errors", "glitchtip", `{"dsn":"https://changed@errors.example/9"}`); err != nil {
					t.Fatal(err)
				}
			})
			if verdict != "pass" {
				t.Fatalf("unchanged incarnation judged with later attachments: %s: %s", verdict, detail)
			}
			// A pass because nothing was given proves nothing: the check judged
			// the DSN the preview booted with.
			if strings.Contains(detail, "nothing attached gives the page a value") {
				t.Fatalf("the check never saw the booted values: %s", detail)
			}
		})
	}
}

// dist can be the declared static source. A run's preview boots the run's
// commit; once the run commits a change to its static output, the preview
// of the earlier commit is not the checked commit, and is not judged.
func TestRuntimeChecksNeverJudgeAPreviewOfAnEarlierCommit(t *testing.T) {
	t.Parallel()
	before := func(_ *App, _ ids.ID, ws string, _ *fakePreviewWorker) {
		if err := os.MkdirAll(filepath.Join(ws, "dist"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(ws, "dist/index.html"), []byte("old static page"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(ws, ".apptown/app.yaml"), []byte("app:\n  web: {static: dist, public: true}\n"), 0644); err != nil {
			t.Fatal(err)
		}
		commitFolder(t, ws)
	}
	after := func(_ *App, _ ids.ID, ws string, _ *fakePreviewWorker) {
		if err := os.WriteFile(filepath.Join(ws, "dist/index.html"), []byte("different static page"), 0644); err != nil {
			t.Fatal(err)
		}
		commitFolder(t, ws)
	}
	_, verdict, detail := runtimeGate(t, gatePlace{onWorker: true}, before, after)
	if verdict != "cannot-assess" {
		t.Fatalf("preview of old static output judged new tree: %s: %s", verdict, detail)
	}
}

// Read permission must not let a denied preview request publish a chat's commits.
func TestADeniedPreviewSavesNothing(t *testing.T) {
	t.Parallel()
	w, _, s := rehearsalWorld(t)
	w.a.addGrant("user", "reader", access.RoleAuditor, "", access.ScopeProject, idstest.N(1).String(), "fixture", "fixture", "")
	before := w.branchTip(t)
	resp := authzCall(w.a.Handler(), "reader", "POST", fmt.Sprintf("/api/v1/changes/%d/preview", w.change.ID), `{}`)
	if resp.Code != 403 {
		t.Fatalf("expected denied preview: %d %s", resp.Code, resp.Body.String())
	}
	if after := w.branchTip(t); after != before {
		t.Fatalf("denied preview saved chat %s: branch advanced %s -> %s", s.ID, before, after)
	}
}

// The contract used to judge a preview is the contract it booted: the run's
// commit, whatever the run's folder holds besides. A static site committed
// and booted is judged as a static site even when the folder's uncommitted
// contract says it is a server.
func TestRuntimeChecksJudgeTheContractThePreviewBooted(t *testing.T) {
	t.Parallel()
	for _, onWorker := range []bool{false, true} {
		t.Run(fmt.Sprint(onWorker), func(t *testing.T) {
			before := func(_ *App, _ ids.ID, ws string, _ *fakePreviewWorker) {
				path := filepath.Join(ws, ".apptown/app.yaml")
				if err := os.WriteFile(path, []byte("app:\n  web: {static: ., public: true}\n"), 0644); err != nil {
					t.Fatal(err)
				}
				commitFolder(t, ws)
				// Not committed: neither booted nor checked.
				if err := os.WriteFile(path, []byte("app:\n  web: {image: 'app:1', ports: ['8080'], public: true}\n"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			_, verdict, detail := runtimeGate(t, gatePlace{onWorker: onWorker}, before, nil)
			if verdict != "pass" || strings.Contains(detail, "server app") {
				t.Fatalf("the static preview was not judged as the static site it booted: %s: %s", verdict, detail)
			}
		})
	}
}

// Losing the removal request before it reaches the worker must leave a
// retryable cleanup, not a successful completion with a still-public preview.
func TestACleanupTheWorkerDidNotConfirmIsAnError(t *testing.T) {
	t.Parallel()
	a, _, fake, ch := previewWorkerWorld(t, false)
	resp := authzCall(a.Handler(), "owner", "POST", fmt.Sprintf("/api/v1/changes/%d/preview", ch.ID), `{}`)
	if resp.Code != 200 {
		t.Fatalf("boot: %d %s", resp.Code, resp.Body.String())
	}
	fake.setDown(true)
	err := workResourcesFor(a).Change(context.Background(), "game", ch.ID)
	fake.setDown(false)
	st, stateErr := a.workerPreviews.State(context.Background(), docker.PreviewChangeName(ch.ID))
	if stateErr != nil || !st.Running {
		t.Fatalf("fixture lost the running preview: %+v %v", st, stateErr)
	}
	if err == nil {
		t.Fatal("change cleanup acknowledged success after its worker removal never arrived; the preview still runs")
	}
}

// A rehearsal never leaves a preview for a change that closed: one queued
// before the change closed boots nothing, and one whose change closes while
// it boots removes what it booted.
func TestARehearsalLeavesNoPreviewForAClosedChange(t *testing.T) {
	t.Parallel()
	close := func(w workerChatWorld) {
		if _, err := w.a.Work.Abandon(asPerson("owner"), workflow.StopWork{ChangeID: w.change.ID, Reason: "closed during a rehearsal"}); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("closed before it runs", func(t *testing.T) {
		t.Parallel()
		w, f, s := rehearsalWorld(t)
		saved := saveWithoutRehearsal(t, w, s)
		close(w)
		w.a.changeConsole.RehearseSaved(context.Background(), s, saved)
		if n := len(f.bootsNamed(docker.PreviewChangeName(w.change.ID))); n != 0 {
			t.Fatalf("a closed change's preview booted %d times", n)
		}
	})
	t.Run("closed while it boots", func(t *testing.T) {
		t.Parallel()
		w, f, s := rehearsalWorld(t)
		saved := saveWithoutRehearsal(t, w, s)
		name := docker.PreviewChangeName(w.change.ID)
		f.mu.Lock()
		f.answer = func(spec worker.PreviewSpec) worker.PreviewState {
			close(w)
			return worker.PreviewState{Running: true, ContainerID: "closed", Incarnation: spec.Incarnation}
		}
		f.mu.Unlock()
		before := len(f.removals())
		w.a.changeConsole.RehearseSaved(context.Background(), s, saved)
		if len(f.bootsNamed(name)) != 1 || !slices.Contains(f.removals()[before:], name) {
			t.Fatalf("a preview booted for a change that closed meanwhile was not removed: boots %d, removed %q", len(f.bootsNamed(name)), f.removals())
		}
	})
	t.Run("closed while it boots, and its removal lost", func(t *testing.T) {
		t.Parallel()
		w, f, s := rehearsalWorld(t)
		saved := saveWithoutRehearsal(t, w, s)
		name := docker.PreviewChangeName(w.change.ID)
		f.mu.Lock()
		f.answer = func(spec worker.PreviewSpec) worker.PreviewState {
			close(w)
			f.down = true // the boot answered; the removal after it never arrives
			return worker.PreviewState{Running: true, ContainerID: "closed", Incarnation: spec.Incarnation}
		}
		f.mu.Unlock()
		w.a.changeConsole.RehearseSaved(context.Background(), s, saved)
		f.setDown(false)
		if st, err := w.a.workerPreviews.State(context.Background(), name); err != nil || !st.Running {
			t.Fatalf("fixture: the late preview is not running: %+v %v", st, err)
		}
		if err := w.a.Preview.ReconcileWorkerPreviews(context.Background()); err != nil {
			t.Fatal(err)
		}
		if st, err := w.a.workerPreviews.State(context.Background(), name); err != nil || st.Running {
			t.Fatalf("a closed change's late preview survived the reconciliation: %+v %v", st, err)
		}
	})
}

// A preview step that passes its front-page check records what the check
// saw, here and on the worker: a pass is witnessed, not assumed.
func TestAPassingPreviewRecordsItsFrontPageCheck(t *testing.T) {
	t.Parallel()
	for _, onWorker := range []bool{false, true} {
		t.Run(fmt.Sprint(onWorker), func(t *testing.T) {
			var page map[string]any
			place := gatePlace{onWorker: onWorker, booted: func(boot *workflow.Step) {
				page, _ = stepEvidence(t, boot)["frontPage"].(map[string]any)
			}}
			before := func(_ *App, _ ids.ID, _ string, f *fakePreviewWorker) {
				if f == nil {
					return
				}
				f.mu.Lock()
				f.answer = func(spec worker.PreviewSpec) worker.PreviewState {
					return worker.PreviewState{Running: true, Incarnation: spec.Incarnation, ContainerID: "c1", BrowserValues: map[string]string{},
						Page: &docker.PageWitness{Path: "/", Host: "run-00000000-0001-7000-8000-000000000000.preview.example.com", Status: 200, Passed: true, Verdict: "GET / answered 200"}}
				}
				f.mu.Unlock()
			}
			runtimeGate(t, place, before, nil)
			if page == nil || page["passed"] != true || page["path"] != "/" || page["status"] != float64(200) || page["host"] == "" || page["verdict"] == "" {
				t.Fatalf("the passing front-page check left no witness: %v", page)
			}
		})
	}
}

// saveWithoutRehearsal saves a chat on the worker as a landing does, which
// queues no rehearsal of its own, and answers the commit it saved.
func saveWithoutRehearsal(t *testing.T, w workerChatWorld, s *console.Session) string {
	t.Helper()
	saved, err := w.a.chatSaves.SaveAndReport(context.Background(), scm.ChatHolder(s.ID))
	if err != nil {
		t.Fatal(err)
	}
	return saved
}

// onePixelPNG is a real 1×1 PNG.
const onePixelPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="

// shotsAnswer is the driver's report a capture answers with
// (workerCheckDocker): two pictures of the home page, and a named page that
// left the preview.
const shotsAnswer = `{"pages":[{"name":"1-desktop","png":"` + onePixelPNG + `"},{"name":"1-phone","png":"` + onePixelPNG + `"},{"name":"2-desktop","reason":"left the preview for https://elsewhere.example/"}]}`

// bootWithShots boots a run's preview on an AppTown with a preview domain
// and reads the boot step back.
func bootWithShots(t *testing.T, report string) (*App, *workflow.Step) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("<h1>app</h1>")) }))
	t.Cleanup(server.Close)
	daemon := newFakeDocker()
	daemon.port = server.Listener.Addr().(*net.TCPAddr).Port
	app := testApp(t, Config{DockerClient: daemon, DockerProbe: &docker.Probe{Settle: time.Millisecond},
		TierDomains: []string{"preview=preview.example.com"}, ProcessRunner: execution.Runner{DockerBinary: workerCheckDocker(t, filepath.Join(t.TempDir(), "calls"))}})
	stages := []schema.Stage{{Name: "wire", Steps: []schema.Step{{Name: "boot", Uses: "preview"}, {Name: "review", Uses: "agent/review", With: map[string]any{"of": "change-report"}}, {Name: "hold", Uses: "approval"}}}}
	a, ctx, ch := changeScenarioIn(t, app, schema.Spec{Kind: "pipeline", Role: "working", Stages: stages})
	rebuildWork(t, a, func(deps *workflow.Dependencies) {
		deps.Automatic = []string{"preview"}
		deps.Runners = a.workDependencies().Runners
		deps.Report = a.workDependencies().Report
	})
	a.Work.Stop()
	run, err := a.Work.StartRun(ctx, workflow.StartChangeRun{ChangeID: ch.ID, Workflow: "test"})
	if !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatal(err)
	}
	if report != "" {
		// The implementer's report names a page (an agent step's output a
		// review audits; here written as the run's change report).
		if _, err := workflow.AddArtifactIn(ctx, a.DB, run.ID, "change-report", "change-report", "", report, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	source, err := workflow.ReadSourceIn(context.Background(), a.DB, workflow.RunRef{ID: run.ID, ProjectID: ch.ProjectID})
	if err != nil {
		t.Fatal(err)
	}
	ws := source.Workspace(a.cfg.dataDir)
	if err := os.MkdirAll(filepath.Join(ws, ".apptown"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, ".apptown", "app.yaml"), []byte("app:\n  web: {static: ., public: true}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	commitFolder(t, ws)
	snap, err := a.Work.Snapshot(ctx, run.ID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	var boot ids.ID
	for _, st := range snap.Steps {
		if st.Name == "boot" {
			boot = st.ID
		}
	}
	if err := a.Work.ExecuteStep(a.asEngine(context.Background()), boot); err != nil {
		t.Fatal(err)
	}
	step, err := workflow.ReadStepIn(context.Background(), a.DB, boot)
	if err != nil {
		t.Fatal(err)
	}
	return a, step
}

type shotsEvidence struct {
	Commit      string
	Screenshots struct {
		Commit string
		Shots  []struct {
			Artifact ids.ID
			Path     string
			View     string
		}
		Missing     []string
		Unavailable string
	}
}

// A boot that passes records AppTown's pictures of the preview it booted,
// from the commit it booted, each a picture the run keeps.
func TestABootRecordsScreenshotsOfThePreviewItBooted(t *testing.T) {
	t.Parallel()
	a, step := bootWithShots(t, "Changed the page.\n\nPages: /orders\n")
	if step.Status != workflow.StatusPass {
		t.Fatalf("boot: %s %s", step.Status, step.Evidence)
	}
	var ev shotsEvidence
	if err := json.Unmarshal(step.Evidence, &ev); err != nil {
		t.Fatal(err)
	}
	if ev.Commit == "" || ev.Screenshots.Commit != ev.Commit || len(ev.Screenshots.Shots) != 2 || ev.Screenshots.Unavailable != "" {
		t.Fatalf("evidence: %s", step.Evidence)
	}
	if !strings.Contains(strings.Join(ev.Screenshots.Missing, ";"), "/orders · desktop: left the preview") {
		t.Fatalf("the named page's reason was lost: %v", ev.Screenshots.Missing)
	}
	png, _ := base64.StdEncoding.DecodeString(onePixelPNG)
	for _, s := range ev.Screenshots.Shots {
		got, err := a.Work.Artifact(a.asEngine(context.Background()), s.Artifact)
		if err != nil {
			t.Fatal(err)
		}
		if got.Kind != "screenshot" || !got.Binary || string(got.Raw) != string(png) || got.RunID != step.RunID {
			t.Fatalf("picture %s: kind %q binary %v %d bytes", s.Artifact, got.Kind, got.Binary, len(got.Raw))
		}
	}
}

// A capture that fails is said, and the boot still passed: the app came up.
func TestABootWhoseScreenshotsFailSaysSoAndStillPasses(t *testing.T) {
	t.Parallel()
	_, step := bootWithShots(t, "Pages: /shots-fail\n")
	var ev shotsEvidence
	if err := json.Unmarshal(step.Evidence, &ev); err != nil {
		t.Fatal(err)
	}
	if step.Status != workflow.StatusPass || ev.Screenshots.Unavailable == "" || len(ev.Screenshots.Shots) != 0 {
		t.Fatalf("boot: %s %s", step.Status, step.Evidence)
	}
}
