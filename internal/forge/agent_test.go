package forge

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"flokkit.io/forge/internal/forge/operations"
	"flokkit.io/forge/internal/forge/projectlifecycle"
	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/workflow"
)

// ---- RunAgent through the fake-pi seam --------------------------------------

// An agent or review step's own guarantees (sessions, refusals, empty turns,
// evidence, reports, settlement, transcripts) are workflowexec's
// (agent_step_test.go, transcripts_test.go, review_step_test.go). These
// fixtures serve the root's tests of the agent runner as the root composes
// it: the CLI and run token, the ways library, private Docker, project
// deletion and the worker journeys.

func sessionIDOf(req ExecRequest) string {
	for i, x := range req.Argv {
		if x == "--session-id" && i+1 < len(req.Argv) {
			return req.Argv[i+1]
		}
	}
	return ""
}

// admittedAgentStep admits a run whose one stage is the agent step "build"
// (config is its with:) and also, after it, the checks also.
func admittedAgentStep(t *testing.T, a *App, config string, also ...schema.Step) (ids.ID, ids.ID) {
	t.Helper()
	var with map[string]any
	if err := json.Unmarshal([]byte(config), &with); err != nil {
		t.Fatal(err)
	}
	p, err := a.lifecycle.Create(context.Background(), projectlifecycle.Draft{Slug: "p", Name: "p", Actor: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.lifecycle.InitializeRepository(t.Context(), p, "", ""); err != nil {
		t.Fatal(err)
	}
	defineWorkflows(t, a, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "agent", Version: 1}, Spec: schema.Spec{
		Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "work", Steps: append([]schema.Step{{Name: "build", Uses: "agent/pi", With: with}}, also...)}},
	}})
	a.Work.Stop()
	ctx := asPerson("alice")
	change, err := a.Work.Create(ctx, workflow.ChangeDraft{ProjectID: p.ID, Title: "agent work"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := a.Work.StartRun(ctx, workflow.StartChangeRun{ChangeID: change.ID, Workflow: "agent"})
	if !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatal(err)
	}
	snapshot, err := a.Work.Snapshot(ctx, run.ID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range snapshot.Steps {
		if step.Name == "build" && step.Type == "agent/pi" && step.Status == workflow.StatusPending {
			return run.ID, step.ID
		}
	}
	t.Fatalf("pending agent step not materialized: %+v", snapshot.Steps)
	return ids.Nil, ids.Nil
}

// piKeepsSession stands in for Pi's session file: it appends one turn to the
// file in the step's session directory whose header names the invocation's
// session, creating the file (header first) when none does, as Pi does. It
// returns what that file held before the turn.
func piKeepsSession(t *testing.T, req ExecRequest, text string) string {
	t.Helper()
	session := sessionIDOf(req)
	dir := filepath.Join(req.Workspace, ".forge", "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	file := ""
	matches, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	for _, m := range matches {
		f, err := os.Open(m)
		if err != nil {
			t.Fatal(err)
		}
		first, _ := bufio.NewReader(f).ReadString('\n')
		f.Close()
		var h struct{ Type, ID string }
		if json.Unmarshal([]byte(first), &h) == nil && h.Type == "session" && h.ID == session {
			file = m
		}
	}
	before := ""
	if file == "" {
		file = filepath.Join(dir, fmt.Sprintf("2026-10-01T00-00-%02d-000Z_%s.jsonl", len(matches), session))
		before = fmt.Sprintf(`{"type":"session","version":3,"id":%q,"timestamp":"2026-10-01T00:00:00.000Z","cwd":"/work"}`+"\n", session)
		if err := os.WriteFile(file, []byte(before), 0o644); err != nil {
			t.Fatal(err)
		}
	} else {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		before = string(raw)
	}
	f, err := os.OpenFile(file, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, `{"type":"message","message":{"role":"assistant","content":[{"type":"text","text":%q}]}}`+"\n", text); err != nil {
		t.Fatal(err)
	}
	return before
}

// keptTranscript reads the bytes an attempt's receipt names.
func keptTranscript(t *testing.T, a *App, runID ids.ID, stepID ids.ID, attempt int64) (string, error) {
	t.Helper()
	run, err := workflow.ReadRunIn(context.Background(), a.DB, runID)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := workflow.StepTranscriptIn(context.Background(), a.DB, workflow.RunRef{ID: run.ID, ProjectID: run.ProjectID}, stepID, attempt)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := workflow.ReadTranscript(a.cfg.dataDir, receipt)
	return string(raw), err
}

// A deleted project's cleanup removes the transcripts its receipts kept,
// through the plan fixed when its records went.
func TestProjectCleanupRemovesItsKeptTranscripts(t *testing.T) {
	t.Parallel()
	a := agentTestApp(t, Config{DockerClient: newFakeDocker()})
	runID, stepID := admittedAgentStep(t, a, `{"prompt":"do it"}`)
	installFakePi(t, a, func(_ int, req ExecRequest) ExecOutcome {
		piKeepsSession(t, req, "done")
		return ExecOutcome{Output: piSays("done")}
	})
	a.Work.ExecuteStep(a.asEngine(context.Background()), stepID)
	if _, err := keptTranscript(t, a, runID, stepID, 1); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(a.cfg.dataDir, "transcripts", "*.jsonl"))
	if len(files) != 1 {
		t.Fatalf("kept files: %v", files)
	}
	run, err := workflow.ReadRunIn(context.Background(), a.DB, runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status == workflow.RunRunning {
		if _, err := a.Work.Abandon(asPerson("alice"), workflow.StopWork{RunID: runID, Reason: "done with it"}); err != nil {
			t.Fatal(err)
		}
	}
	p, err := a.Projects.ByID(context.Background(), run.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.deletion.Request(asPerson("alice"), p.ID); err != nil {
		t.Fatal(err)
	}
	passDeletion(t, a, p.ID)
	if op := purgeWhenDue(t, a, p.ID); op.State != operations.Succeeded {
		t.Fatalf("purge: %s %q", op.State, op.Detail)
	}
	if _, err := os.Stat(files[0]); err != nil {
		t.Fatalf("removed before the cleanup ran: %v", err)
	}
	runCleanup(t, a, run.ProjectID, 0)
	if _, err := os.Stat(files[0]); !os.IsNotExist(err) {
		t.Fatalf("the deleted project's transcript remains: %v", err)
	}
}
