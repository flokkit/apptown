package apptown

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/agentharness/pi/pitest"

	"github.com/flokkit/apptown/internal/execution"

	"github.com/flokkit/apptown/internal/apptown/agentenv"
	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/projectlifecycle"

	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow"
)

// ---- the fake-pi seam -------------------------------------------------------

// fakePi stands in for piRunner: it records every ExecRequest and answers
// each with whatever the scenario scripts for that call, so RunAgent and
// RunAgentReview run their outcome logic without Docker or a model.
type fakePi struct {
	mu    sync.Mutex
	calls []execRequest
	on    func(n int, req execRequest) execOutcome // n is 1-based
}

func installFakePi(t *testing.T, a *App, on func(n int, req execRequest) execOutcome) *fakePi {
	t.Helper()
	f := &fakePi{on: on}
	a.Work.Stop()
	a.agents = agentExecutionFor(a, f)
	rebuildWork(t, a, func(*workflow.Dependencies) {})
	a.Work.Stop()
	return f
}

// Run answers req as on says, as pi run through its capture
// (piharness.CapturedArgv) would: the stream it prints is written to the
// events file the argv names, which is where the result is read; on sees
// pi's own argv.
func (f *fakePi) Run(_ context.Context, _ string, req execRequest) execOutcome {
	events, inner, captured := pitest.CapturedEvents(req.Argv)
	if captured {
		req.Argv = inner
	}
	f.mu.Lock()
	f.calls = append(f.calls, req)
	n := len(f.calls)
	f.mu.Unlock()
	out := f.on(n, req)
	if captured && (out.Termination == execution.Exited || out.Termination == "") {
		_ = os.WriteFile(filepath.Join(req.Workspace, filepath.FromSlash(events)), out.Output, 0o644)
	}
	return out
}

func (f *fakePi) requests() []execRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]execRequest(nil), f.calls...)
}

// piSays is a pi --mode json transcript with one assistant turn of text.
func piSays(text string) []byte {
	ev := map[string]any{"type": "message_end", "message": map[string]any{
		"role": "assistant", "model": "fake-model", "provider": "fake",
		"content": []map[string]any{{"type": "text", "text": text}},
		"usage":   map[string]any{"input": 10, "output": 5, "totalTokens": 15},
	}}
	return append(mustJSONBytes(ev), '\n')
}

func mustJSONBytes(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// agentTestApp is a testApp wired for pi-free runs: a pre-built "CLI" so
// the mount is offered, and
// automatic dispatch stopped so a failing verdict never spawns a real agent.
func agentTestApp(t *testing.T, configs ...Config) *App {
	t.Helper()
	cfg := Config{}
	if len(configs) != 0 {
		cfg = configs[0]
	}
	cfg.AgentImage = "apptown-agent"
	if cfg.ConsoleCLI == "" {
		cfg.ConsoleCLI = fixtureCLI(t)
	}
	a := testApp(t, cfg)

	a.Work.Stop()
	return a
}

func hasMount(req execRequest, suffix string) bool {
	for _, m := range req.Mounts {
		if strings.HasSuffix(m, suffix) {
			return true
		}
	}
	return false
}

func envValue(req execRequest, key string) string {
	for _, e := range req.Env {
		if v, ok := strings.CutPrefix(e, key+"="); ok {
			return v
		}
	}
	return ""
}

func stepStatus(t *testing.T, a *App, stepID ids.ID) (workflow.StepState, map[string]any) {
	t.Helper()
	st, err := workflow.ReadStepIn(context.Background(), a.DB, stepID)
	if err != nil {
		t.Fatal(err)
	}
	var ev map[string]any
	_ = json.Unmarshal(st.Evidence, &ev)
	return st.Status, ev
}

func admittedReviewStep(t *testing.T, a *App, slug string) (ids.ID, ids.ID) {
	t.Helper()
	p, err := a.lifecycle.Create(context.Background(), projectlifecycle.Draft{Slug: slug, Name: slug, Actor: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.lifecycle.InitializeRepository(t.Context(), p, "", ""); err != nil {
		t.Fatal(err)
	}
	defineWorkflows(t, a, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "review", Version: 1}, Spec: schema.Spec{
		Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "work", Steps: []schema.Step{{Name: "review", Uses: "agent/review"}}}},
	}})
	a.Work.Stop()
	ctx := asPerson("alice")
	change, err := a.Work.Create(ctx, workflow.ChangeDraft{ProjectID: p.ID, Title: "review the change"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := a.Work.StartRun(ctx, workflow.StartChangeRun{ChangeID: change.ID, Workflow: "review"})
	if !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatal(err)
	}
	snapshot, err := a.Work.Snapshot(ctx, run.ID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range snapshot.Steps {
		if step.Name == "review" && step.Type == "agent/review" && step.Status == workflow.StatusPending {
			return run.ID, step.ID
		}
	}
	t.Fatalf("pending review step not materialized: %+v", snapshot.Steps)
	return ids.Nil, ids.Nil
}

func writeVerdict(t *testing.T, ws, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(ws, ".apptown", "review.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ---- the review -------------------------------------------------------------

// Every reviewer invocation — the initial run, the wrap-up resume and the
// fixup — must carry AppTown CLI mount and a LIVE run token: the
// reviewer verifies claims like "apptown check passed" with them. The
// resume and fixup copies must pass the same mounts, or they lose the CLI.
func TestReviewInvocationsAllCarryCLIAndRunToken(t *testing.T) {
	t.Parallel()
	check := func(t *testing.T, a *App, runID ids.ID, reqs []execRequest) {
		t.Helper()
		for i, req := range reqs {
			if !hasMount(req, ":"+agentenv.CLIDir+"/apptown:ro") {
				t.Errorf("call %d: no AppTown CLI mount in %v", i+1, req.Mounts)
			}
			tok := envValue(req, "APPTOWN_SESSION_TOKEN")
			if tok == "" {
				t.Errorf("call %d: no APPTOWN_SESSION_TOKEN in env", i+1)
			}
			if envValue(req, "APPTOWN_RUN_ID") != fmt.Sprint(runID) || envValue(req, "APPTOWN_STEP") != "review" {
				t.Errorf("call %d: APPTOWN_RUN_ID/APPTOWN_STEP wrong for run %d: %v", i+1, runID, req.Env)
			}
			// pi runs directly, or as the command the toolchain wrapper execs
			// (sh -c '<install>; exec "$@"' sh pi ...).
			if req.Argv[0] != "pi" && !(req.Argv[0] == "sh" && reviewHasArg(req, "pi")) {
				t.Errorf("call %d: argv %v does not run pi", i+1, req.Argv)
			}
		}
		if len(reqs) >= 2 {
			// Same env every time — the token identical, nothing dropped.
			if envValue(reqs[0], "APPTOWN_SESSION_TOKEN") != envValue(reqs[1], "APPTOWN_SESSION_TOKEN") {
				t.Error("the resume ran with a different run token than the initial invocation")
			}
			if !reviewHasArg(reqs[1], "--continue") || reviewHasArg(reqs[0], "--continue") {
				t.Errorf("only the follow-up call resumes the session: %v / %v", reqs[0].Argv, reqs[1].Argv)
			}
		}
	}

	t.Run("no verdict then resume", func(t *testing.T) {
		t.Parallel()
		a := agentTestApp(t)
		runID, stepID := admittedReviewStep(t, a, "p")
		var liveTokens []ids.ID
		f := installFakePi(t, a, func(n int, req execRequest) execOutcome {
			// The token must be LIVE while pi runs, not just present.
			who, _ := inferenceIdentityFor(a).AuthenticateStep(context.Background(), envValue(req, "APPTOWN_SESSION_TOKEN"))
			liveTokens = append(liveTokens, who.RunID)
			if n == 2 {
				writeVerdict(t, req.Workspace, `{"verdict":"pass","summary":"all claims hold","findings":[]}`)
			}
			return execOutcome{Output: piSays("working")}
		})
		a.Work.ExecuteStep(a.asEngine(context.Background()), stepID)
		reqs := f.requests()
		if len(reqs) != 2 {
			t.Fatalf("want initial + resume, got %d calls", len(reqs))
		}
		check(t, a, runID, reqs)
		for i, who := range liveTokens {
			if who != runID {
				t.Errorf("call %d: run token did not resolve to the run while pi ran (got %d)", i+1, who)
			}
		}
		status, ev := stepStatus(t, a, stepID)
		if status != workflow.StatusPass || ev["verdict"] != "pass" || ev["resumed"] != true {
			t.Fatalf("want pass via resume, got %s %v", status, ev)
		}
	})

	t.Run("invalid verdict then fixup", func(t *testing.T) {
		t.Parallel()
		a := agentTestApp(t)
		runID, stepID := admittedReviewStep(t, a, "p")
		f := installFakePi(t, a, func(n int, req execRequest) execOutcome {
			switch n {
			case 1:
				writeVerdict(t, req.Workspace, `{"verdict":"pa`) // truncated mid-write
			case 2:
				writeVerdict(t, req.Workspace, `{"verdict":"fail","summary":"claims X, diff lacks X","findings":["X missing"]}`)
			}
			return execOutcome{Output: piSays("working")}
		})
		a.Work.ExecuteStep(a.asEngine(context.Background()), stepID)
		reqs := f.requests()
		if len(reqs) != 2 {
			t.Fatalf("want initial + fixup, got %d calls", len(reqs))
		}
		check(t, a, runID, reqs)
		if !strings.HasSuffix(reqs[1].Name, "-fixup") {
			t.Errorf("second call should be the fixup, got %q", reqs[1].Name)
		}
		status, ev := stepStatus(t, a, stepID)
		if status != workflow.StatusFail || ev["verdict"] != "fail" || ev["resumed"] != true ||
			!strings.Contains(fmt.Sprint(ev["reason"]), "reviewer rejected") {
			t.Fatalf("want fail via fixup, got %s %v", status, ev)
		}
	})
}

func reviewHasArg(req execRequest, arg string) bool {
	for _, x := range req.Argv {
		if x == arg {
			return true
		}
	}
	return false
}
