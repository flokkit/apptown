package forge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/forge/preview"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/console"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/forge/conversation"
	"flokkit.io/forge/internal/forge/httpapi"
	"flokkit.io/forge/internal/forge/mcpapi"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/verification"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/worker"
)

// The composed proof of previews on a worker: a forge composed with a worker, as startup composes it (Config.Worker),
// whose own Docker is a tripwire, runs every preview caller on the worker.

// errTripwire is this machine's Docker refusing what a preview would ask
// of it: a preview on the worker never gets that far here.
var errTripwire = errors.New("the tripwire refused a preview command on this machine")

// previewTripwire is this machine's Docker for the proof: it records every
// command, and refuses every one that would boot, reach or inspect a preview
// here (listing, and removal a cleanup asks for, are harmless).
func previewTripwire() *fakeDocker {
	f := newFakeDocker()
	f.reply = func(args []string) (string, error, bool) {
		if bootsOrReachesHere(strings.Join(args, " ")) {
			return "", errTripwire, true
		}
		return "", nil, false
	}
	return f
}

// bootsOrReachesHere says whether a Docker command boots, builds, pulls,
// runs or reaches a preview on this machine.
func bootsOrReachesHere(call string) bool {
	verb, _, _ := strings.Cut(call, " ")
	switch verb {
	case "run", "build", "pull", "create", "start", "exec", "port", "compose", "buildx":
		return true
	case "rm", "ps", "network", "volume":
		return false
	}
	return strings.Contains(call, "forge-preview-")
}

// nothingRanHere fails when this machine's Docker was asked to boot, build,
// pull, run or reach anything for a preview.
func nothingRanHere(t *testing.T, local *fakeDocker) {
	t.Helper()
	for _, call := range local.joined() {
		if bootsOrReachesHere(call) {
			t.Fatalf("a preview touched this machine's Docker: %s\n%s", call, strings.Join(local.joined(), "\n"))
		}
	}
}

// proofWorld is previewWorkerWorld with the tripwire as this machine's
// Docker.
func proofWorld(t *testing.T) (*App, *fakeDocker, *fakePreviewWorker, *workflow.Change) {
	t.Helper()
	fake := &fakePreviewWorker{}
	local := previewTripwire()
	a := workAuthorityFixture(t, withPreviewWorker(t, fake, Config{DockerClient: local, DockerProbe: &docker.Probe{Settle: 1},
		TierDomains: []string{"preview=preview.example.com"}}))
	if !a.Preview.PreviewsOnWorker() {
		t.Fatal("an AppTown composed with a worker keeps its previews on this machine")
	}
	ch, err := a.Work.Create(asPerson("owner"), workflow.ChangeDraft{ProjectID: idstest.N(1), Title: "The proof"})
	if err != nil {
		t.Fatal(err)
	}
	folder := filepath.Join(a.cfg.dataDir, "workspaces", fmt.Sprintf("change-%d", ch.ID))
	if err := os.MkdirAll(filepath.Join(folder, ".forge"), 0o755); err != nil {
		t.Fatal(err)
	}
	for path, body := range map[string]string{".forge/app.yaml": staticSiteContract, "index.html": "<html>the game</html>"} {
		if err := os.WriteFile(filepath.Join(folder, path), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return a, local, fake, ch
}

// consolePreview asks for a preview through the console's door
// (POST /api/v1/console/{sid}/preview), as a change chat's `forge preview`
// does, with the chat's session token.
func consolePreview(t *testing.T, a *App, session *console.Session) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("POST", "/api/v1/console/"+session.ID+"/preview", nil)
	r.SetPathValue("sid", session.ID)
	r.Header.Set("X-Forge-Session-Token", session.Token)
	out := httptest.NewRecorder()
	httpapi.NewConsolePreview(a.Console, a.Preview, a.DB, a.cfg.publicHost, nil).ConsolePreview(out, r.WithContext(callerContext(r)))
	return out
}

func ownerConsole(a *App, id string, ch *workflow.Change) *console.Session {
	meta := consoleMetaFor(id, ch.ProjectID, ch.ID)
	meta.Actor, meta.Token = "owner", id+"-session-token"
	return a.Console.Restore(meta)
}

// proofGate runs a change's lane on a forge composed with the fake worker
// and the tripwire: the run's preview step boots on the worker, afterBoot
// does what it does, and the guidance step judges a readiness probe and
// the page's browser values against that preview. It answers the gate and
// each check's verdict and detail, by name.
func proofGate(t *testing.T, fake *fakePreviewWorker, local *fakeDocker, afterBoot func()) (*workflow.Step, map[string][2]string, ids.ID) {
	t.Helper()
	const given = `{"GLITCHTIP_DSN":"https://key@errors.example/3","SENTRY_DSN":"https://key@errors.example/3"}`
	const loads = `<html><head><script src="/_forge/env.js"></script><script src="game.js"></script></head></html>`
	fake.app = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/_forge/env.js" {
			w.Write([]byte(docker.BrowserEnvScript(given)))
			return
		}
		w.Write([]byte(loads))
	})
	var values map[string]string
	_ = json.Unmarshal([]byte(given), &values)
	fake.answer = func(spec worker.PreviewSpec) worker.PreviewState {
		return worker.PreviewState{Running: true, Incarnation: spec.Incarnation, ContainerID: "c1", BrowserValues: values}
	}
	app := testApp(t, withPreviewWorker(t, fake, Config{DockerClient: local, DockerProbe: &docker.Probe{Settle: time.Millisecond},
		TierDomains: []string{"preview=preview.example.com"}}))
	app.SyncStockDependencies(context.Background()) // error reporting's definition says what the page is given
	stages := []schema.Stage{{Name: "wire", Steps: []schema.Step{{Name: "boot", Uses: "preview"}, {Name: "guidance-conformance", Uses: "guidance/conformance"}}}}
	a, ctx, ch := changeScenarioIn(t, app, schema.Spec{Kind: "pipeline", Role: "working", Stages: stages})
	// Readiness is below the gate's threshold: its front door is the
	// preview domain's, which a test cannot reach; its naked door is.
	adopt(t, a, ch.ProjectID, "deploy/browser-values", 1, `[{"name":"page-loads-browser-values","expects":"loads the public values","evaluator":"runtime","probe":"browser-env","severity":"high"},`+
		`{"name":"answers","expects":"the app answers","evaluator":"runtime","probe":"GET /healthz","severity":"low"}]`)
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
	if err := os.MkdirAll(filepath.Join(ws, ".forge"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, ".forge", "app.yaml"), []byte("app:\n  web: {static: ., public: true}\n"), 0o644); err != nil {
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
	if err := a.Work.ExecuteStep(a.asEngine(context.Background()), boot); err != nil {
		t.Fatal(err)
	}
	if booted, err := workflow.ReadStepIn(context.Background(), a.DB, boot); err != nil || booted.Status != workflow.StatusPass {
		t.Fatalf("the preview step: %v %+v", err, booted)
	}
	// The front door: the forge's own preview proxy, asked by the preview
	// domain's name, reaches the run's preview through the worker.
	front := httptest.NewRequest("GET", "/", nil)
	front.Host = fmt.Sprintf("run-%d.preview.example.com", run.ID)
	rec := httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, front)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "/_forge/env.js") {
		t.Fatalf("the front door did not reach the preview on the worker: %d %s", rec.Code, rec.Body.String())
	}
	if afterBoot != nil {
		afterBoot()
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
	judged := map[string][2]string{}
	for _, r := range evidence.Checks {
		judged[r.Check] = [2]string{r.Verdict.String(), r.Detail}
	}
	return saved, judged, run.ID
}

// With a worker configured, no preview boots on this machine. This
// machine's Docker is a tripwire; every caller boots on the worker: the
// try-it button and its restart, a console's and an MCP preview, a
// workflow's preview step and the runtime checks that probe it, and a chat
// on the worker's rehearsal. Callers without authority reach nothing, and a
// worker that cannot be asked is neither the app's failure nor a pass.
func TestNoPreviewBootsOnThisMachineWhenAWorkerIsConfigured(t *testing.T) {
	t.Parallel()

	t.Run("the try-it button and its restart", func(t *testing.T) {
		t.Parallel()
		a, local, fake, ch := proofWorld(t)
		for _, suffix := range []string{"", "/restart"} {
			resp := authzCall(a.Handler(), "owner", "POST", fmt.Sprintf("/api/v1/changes/%d/preview%s", ch.ID, suffix), `{}`)
			var out struct{ URL string }
			if err := json.Unmarshal(resp.Body.Bytes(), &out); err != nil || resp.Code != 200 || out.URL != fmt.Sprintf("https://change-%d.preview.example.com", ch.ID) {
				t.Fatalf("try it%s: %d %s", suffix, resp.Code, resp.Body.String())
			}
		}
		name := docker.PreviewChangeName(ch.ID)
		if n := len(fake.bootsNamed(name)); n != 2 {
			t.Fatalf("boots on the worker: %d", n)
		}
		if got := strings.Join(fake.removals(), " "); got != name {
			t.Fatalf("the restart removed on the worker: %q", got)
		}
		nothingRanHere(t, local)
	})

	t.Run("a console's and an MCP preview", func(t *testing.T) {
		t.Parallel()
		a, local, fake, ch := proofWorld(t)
		session := ownerConsole(a, "proof-console", ch)
		if resp := consolePreview(t, a, session); resp.Code != 200 {
			t.Fatalf("console: %d %s", resp.Code, resp.Body.String())
		}
		mcp := &mcpapi.Call{Context: callerMCPContext(context.Background(), &authIdentity{User: "owner", Role: "member", Via: "token"}, "owner via mcp/test")}
		got, err := mcpToolsFor(a).ByName("preview_change").Run(mcp, map[string]any{"id": ch.ID.String()})
		if err != nil {
			t.Fatal(err)
		}
		if url := got.(map[string]any)["previewUrl"]; url != fmt.Sprintf("https://change-%d.preview.example.com", ch.ID) {
			t.Fatalf("MCP: %+v", got)
		}
		if n := len(fake.bootsNamed(docker.PreviewChangeName(ch.ID))); n != 2 {
			t.Fatalf("boots on the worker: %d", n)
		}
		nothingRanHere(t, local)
	})

	t.Run("the console's address is one the chat can reach", func(t *testing.T) {
		t.Parallel()
		a, _, _, ch := proofWorld(t)
		resp := consolePreview(t, a, ownerConsole(a, "proof-address", ch))
		var out httpapi.ConsolePreviewAddress
		if err := json.Unmarshal(resp.Body.Bytes(), &out); err != nil || resp.Code != 200 {
			t.Fatalf("console: %d %s", resp.Code, resp.Body.String())
		}
		// A preview on the worker has no port here; the chat reaches it at
		// the preview domain's address, as any visitor does (plan, R3).
		if out.ContainerURL != out.URL || !strings.HasPrefix(out.URL, "https://change-") {
			t.Fatalf("the chat is told %q (port %d), not the preview's address %q", out.ContainerURL, out.Port, out.URL)
		}
	})

	t.Run("a preview step and its runtime checks", func(t *testing.T) {
		t.Parallel()
		fake, local := &fakePreviewWorker{}, previewTripwire()
		saved, judged, runID := proofGate(t, fake, local, nil)
		if saved.Status != workflow.StatusPass {
			t.Fatalf("the gate: %s %s", saved.Status, saved.Evidence)
		}
		if v := judged["page-loads-browser-values"]; v[0] != "pass" {
			t.Fatalf("the browser check: %q", v)
		}
		// Readiness reached the app through the naked door, a relay to the
		// worker; only its front door, the preview domain's, is out of a
		// test's reach.
		if v := judged["answers"]; !strings.Contains(v[1], "naked http://127.0.0.1:") || !strings.Contains(v[1], "/healthz → 200") {
			t.Fatalf("the readiness probe: %q", v)
		}
		if n := len(fake.bootsNamed(docker.PreviewRunName(runID))); n != 1 {
			t.Fatalf("the run's preview booted on the worker %d times", n)
		}
		fake.mu.Lock()
		forwarded := len(fake.forwarded)
		fake.mu.Unlock()
		if forwarded == 0 {
			t.Fatal("no probe reached the preview on the worker")
		}
		nothingRanHere(t, local)
	})

	t.Run("a chat on the worker's rehearsal", func(t *testing.T) {
		t.Parallel()
		fake, local := &fakePreviewWorker{}, previewTripwire()
		w := newWorkerChatWorldConfigured(t, func(cfg *Config) Config {
			cfg.DockerClient = local
			cfg.TierDomains = []string{"preview=preview.example.com"}
			return withPreviewWorker(t, fake, *cfg)
		})
		s, err := w.openOnWorker(t, conversation.ProjectOpen{ChangeID: w.change.ID})
		if err != nil {
			t.Fatal(err)
		}
		if err := w.a.Console.Send(context.Background(), s.ID, "go"); err != nil {
			t.Fatal(err)
		}
		w.commitOnWorker(t, s.ID, ".forge/app.yaml", staticSiteContract)
		w.a.changeConsole.SaveAfterTurn(s.ID)
		saved := w.branchTip(t)
		eventually(t, "the saved commit rehearsed on the worker", func() bool {
			return len(fake.bootsNamed(docker.PreviewChangeName(w.change.ID))) == 1 && rehearsed(s) == saved
		})
		nothingRanHere(t, local)
	})

	t.Run("callers without authority reach nothing", func(t *testing.T) {
		t.Parallel()
		a, local, fake, ch := proofWorld(t)
		if resp := authzCall(a.Handler(), "outsider", "POST", fmt.Sprintf("/api/v1/changes/%d/preview", ch.ID), `{}`); resp.Code == 200 {
			t.Fatalf("an outsider previewed: %d %s", resp.Code, resp.Body.String())
		}
		mcp := &mcpapi.Call{Context: callerMCPContext(context.Background(), &authIdentity{User: "outsider", Role: "member", Via: "token"}, "outsider via mcp/test")}
		if got, err := mcpToolsFor(a).ByName("preview_change").Run(mcp, map[string]any{"id": ch.ID.String()}); err == nil {
			t.Fatalf("an outsider previewed through MCP: %+v", got)
		}
		other, err := a.Work.Create(asPerson("owner"), workflow.ChangeDraft{ProjectID: idstest.N(1), Title: "Another change"})
		if err != nil {
			t.Fatal(err)
		}
		session := ownerConsole(a, "proof-other", ch)
		var denied *access.Denied
		_, err = a.Preview.RequestChangePreview(verifiedContext(context.Background(), conversation.Principal(session)), preview.Selection{ChangeID: other.ID})
		if !errors.As(err, &denied) {
			t.Fatalf("a console previewing another change: %v", err)
		}
		fake.mu.Lock()
		boots := len(fake.boots)
		fake.mu.Unlock()
		if boots != 0 {
			t.Fatalf("a refused caller reached the worker: %d boots", boots)
		}
		nothingRanHere(t, local)
	})

	t.Run("a worker that cannot be asked", func(t *testing.T) {
		t.Parallel()
		a, local, fake, ch := proofWorld(t)
		fake.setDown(true)
		resp := authzCall(a.Handler(), "owner", "POST", fmt.Sprintf("/api/v1/changes/%d/preview", ch.ID), `{}`)
		if resp.Code == 200 || strings.Contains(resp.Body.String(), "didn't come up") || !strings.Contains(resp.Body.String(), "worker") {
			t.Fatalf("try it with the worker down: %d %s", resp.Code, resp.Body.String())
		}
		state := authzCall(a.Handler(), "owner", "GET", fmt.Sprintf("/api/v1/changes/%d/preview", ch.ID), "")
		var st struct{ State, Reason string }
		if err := json.Unmarshal(state.Body.Bytes(), &st); err != nil || st.State != "unavailable" || st.Reason == "" {
			t.Fatalf("the change page with the worker down: %s", state.Body.String())
		}
		nothingRanHere(t, local)

		gateWorker, gateLocal := &fakePreviewWorker{}, previewTripwire()
		saved, judged, _ := proofGate(t, gateWorker, gateLocal, func() { gateWorker.setDown(true) })
		if v := judged["page-loads-browser-values"]; v[0] != "cannot-assess" || !strings.Contains(v[1], "could not be asked") {
			t.Fatalf("the browser check with the worker down: %q", v)
		}
		if saved.Status == workflow.StatusPass {
			t.Fatalf("a gate passed with the worker down: %s", saved.Evidence)
		}
		nothingRanHere(t, gateLocal)
	})
}
