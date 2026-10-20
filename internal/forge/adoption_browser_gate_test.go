package forge

import (
	"context"
	"os"
	"path/filepath"

	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/verification"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/ids"
)

// The browser check judges the change's preview, and the guidance step acts
// on what it establishes: a page that never loads its values is the change's
// to fix; anything the forge supplies and cannot establish holds the step for
// a person, with or without explicit acceptance, and is never rework. A run
// that has booted its preview needs it, so a preview gone missing holds too;
// a run that has not booted one yet, or never will (the landing), records the
// check unjudged.
func TestTheGuidanceStepActsOnWhatTheBrowserCheckEstablishes(t *testing.T) {
	t.Parallel()
	const given = `{"GLITCHTIP_DSN":"https://key@errors.example/3","SENTRY_DSN":"https://key@errors.example/3"}`
	const loads = `<html><head><script src="/_apptown/env.js"></script><script src="game.js"></script></head></html>`
	const never = `<html><script src="game.js"></script></html>`
	booted, gone, lands := browserLane{boots: true, running: true}, browserLane{boots: true}, browserLane{}
	for _, tc := range []struct {
		name              string
		lane              browserLane
		installed, served string
		page              string
		acceptance        bool
		verdict           string
		status            workflow.StepState
	}{
		{"loads its values", booted, given, given, loads, false, "pass", workflow.StatusPass},
		{"never loads them", booted, given, given, never, false, "fail", workflow.StatusFail},
		{"served none of them", booted, given, `{}`, never, false, "cannot-assess", workflow.StatusError},
		{"served none of them, as the change's acceptance", booted, given, `{}`, never, true, "cannot-assess", workflow.StatusError},
		{"started before they were attached", booted, `{}`, `{}`, loads, false, "cannot-assess", workflow.StatusError},
		{"its preview disappeared", gone, given, given, loads, false, "cannot-assess", workflow.StatusError},
		{"a lane that boots no preview", lands, given, given, loads, false, "cannot-assess", workflow.StatusPass},
		{"a lane that boots its preview in a later stage", browserLane{later: true}, given, given, loads, false, "cannot-assess", workflow.StatusPass},
	} {
		t.Run(tc.name, func(t *testing.T) {
			saved := runBrowserGate(t, tc.lane, tc.installed, tc.served, tc.page, tc.acceptance)
			var evidence struct{ Checks verification.Results }
			if err := json.Unmarshal(saved.Evidence, &evidence); err != nil {
				t.Fatal(err)
			}
			verdict := ""
			for _, r := range evidence.Checks {
				if r.Check == "page-loads-browser-values" {
					verdict = r.Verdict.String()
				}
			}
			if verdict != tc.verdict || saved.Status != tc.status {
				t.Fatalf("want %s and step %s, got %s and step %s: %s", tc.verdict, tc.status, verdict, saved.Status, saved.Evidence)
			}
		})
	}
}

// browserLane is the workflow shape a gate runs in: whether it boots a
// preview before the step, whether that preview is still running when the
// step runs, and whether it boots one only in a later stage.
type browserLane struct{ boots, running, later bool }

// runBrowserGate runs one guidance step over a static change whose preview was
// started with installed, serves served, and answers page.
func runBrowserGate(t *testing.T, lane browserLane, installed, served, page string, acceptance bool) *workflow.Step {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/_apptown/env.js" {
			w.Write([]byte(docker.BrowserEnvScript(served)))
			return
		}
		w.Write([]byte(page))
	}))
	t.Cleanup(server.Close)
	_, port, _ := net.SplitHostPort(server.Listener.Addr().String())
	daemon := newFakeDocker()
	daemon.port, _ = strconv.Atoi(port)
	app := testApp(t, Config{DockerClient: daemon, DockerProbe: &docker.Probe{Settle: time.Millisecond}})
	app.SyncStockDependencies(context.Background()) // error reporting's definition says what the page is given
	steps := []schema.Step{{Name: "guidance-conformance", Uses: "guidance/conformance"}}
	if lane.boots {
		steps = append([]schema.Step{{Name: "boot", Uses: "preview"}}, steps...)
	}
	stages := []schema.Stage{{Name: "wire", Steps: steps}}
	if lane.later {
		stages = append(stages, schema.Stage{Name: "runtime", OnEnter: []schema.Step{{Name: "boot", Uses: "preview"}}, Steps: []schema.Step{{Name: "runtime-guidance", Uses: "guidance/conformance"}}})
	}
	a, ctx, ch := changeScenarioIn(t, app, schema.Spec{Kind: "pipeline", Role: "working", Stages: stages})
	if acceptance {
		accepted, err := a.Work.Create(ctx, workflow.ChangeDraft{ProjectID: ch.ProjectID, Type: "remediation", Title: "Repair browser wiring", Acceptance: &workflow.Acceptance{Checks: []workflow.AcceptanceCheck{{Block: "deploy/browser-values", Version: 1, Check: "page-loads-browser-values"}}}})
		if err != nil {
			t.Fatal(err)
		}
		ch = accepted
	}
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
	// Trusted facts, not the response: the change's contract says it is a
	// static site, the attached DSN is what its page must be given, and the
	// preview container says what it was started with.
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
	commitFolder(t, ws) // a check reads a commit
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
	// A lane that boots runs its preview step for real, then the case sets
	// what the preview container was started with, or takes it away.
	if lane.boots {
		if err := a.Work.ExecuteStep(a.asEngine(context.Background()), boot); err != nil {
			t.Fatal(err)
		}
		if booted, err := workflow.ReadStepIn(context.Background(), a.DB, boot); err != nil || booted.Status != workflow.StatusPass {
			t.Fatalf("the preview did not boot: %v %+v", err, booted)
		}
		name := docker.PreviewRunName(run.ID)
		if lane.running {
			daemon.markStartedWithBrowser(name, installed)
		} else if _, err := daemon.run("rm", "-f", name); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.Work.ExecuteStep(a.asEngine(context.Background()), gate); err != nil {
		t.Fatal(err)
	}
	saved, err := workflow.ReadStepIn(context.Background(), a.DB, gate)
	if err != nil {
		t.Fatal(err)
	}
	return saved
}
