package forge

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/forge/wow"
	"flokkit.io/forge/internal/ids"
)

// Reflection reads (trace.go): the trace is the whole self-contained record
// of a run. These pin its shape — steps in order, events forward in time,
// pinned guidance surfaced, the pinned YAML recovered — plus the list and
// audit reads that share the same joins. Default tier, no Docker.

// tv_traceSpec is the two-stage lifecycle every trace test is born from.
func tv_traceSpec() schema.Spec {
	return schema.Spec{Kind: "lifecycle", Stages: []schema.Stage{
		{Name: "work", Steps: []schema.Step{{Name: "do", Uses: "agent/pi", With: map[string]any{"k": 1}}}},
		{Name: "after", Steps: []schema.Step{{Name: "ok", Uses: "approval"}}},
	}}
}

// tv_seedTraceRun admits a declared workflow and completes its agent step, then
// returns (projectID, runID).
func tv_seedTraceRun(t *testing.T, a *App, slug string, spec schema.Spec, withGuidance bool) (ids.ID, ids.ID) {
	t.Helper()
	pid := createReadTestProject(t, a, slug)
	if withGuidance {
		block, err := a.Ways.CreateBlock(t.Context(), "trace/concise", "prefer small diffs", "trace", "fixture")
		if err != nil {
			t.Fatal(err)
		}
		if err := a.Adoption.PutImports(t.Context(), pid, []wow.ResolvedPin{{BlockID: block.ID, Name: block.Name, Version: block.LatestVersion}}); err != nil {
			t.Fatal(err)
		}
		if _, err := a.recordAdoption(a.projectByID(context.Background(), pid), "edit", "fixture guidance", "fixture"); err != nil {
			t.Fatal(err)
		}
	}
	change := createReadTestChange(t, a, pid, "make it blue")
	installFakeGit(t, a, nil)
	rebuildWork(t, a, func(deps *workflow.Dependencies) {
		deps.Automatic = []string{"agent/pi"}
		deps.Report = func(err error) { t.Error(err) }
		deps.Runners = map[string]workflow.Runner{"agent/pi": func(task *workflow.Execution) {
			if _, err := task.Claim(); err != nil {
				t.Error(err)
				return
			}
			if err := task.Finish(workflow.StepOutcome{Status: workflow.StatusPass, Evidence: json.RawMessage(`{"tokens":5}`)}, nil); err != nil {
				t.Error(err)
			}
		}}
	})
	defineWorkflows(t, a, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "build", Version: 1}, Spec: spec})
	run, err := a.Work.StartRun(asPerson("bls"), workflow.StartChangeRun{ChangeID: change, Workflow: "build"})
	if err != nil {
		t.Fatal(err)
	}
	a.Work.Wait()
	return pid, run.ID
}

func tv_getJSON(t *testing.T, h http.Handler, path string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	var out map[string]any
	if w.Body.Len() > 0 && strings.HasPrefix(strings.TrimSpace(w.Body.String()), "{") {
		_ = json.Unmarshal(w.Body.Bytes(), &out)
	}
	return w.Code, out
}

func TestHandleTraceShape(t *testing.T) {
	t.Parallel()
	a := testApp(t)

	h := a.Handler()

	spec := tv_traceSpec()
	pid, rid := tv_seedTraceRun(t, a, "trc", spec, true)

	// Work admission emitted stage.entered and guidance.pinned from the
	// project's immutable adoption receipt.
	expectedHash := fmt.Sprintf("%x", sha256.Sum256([]byte("# trace/concise v1\n\nprefer small diffs")))

	if _, err := a.Work.AttachArtifact(asPerson("bls"), rid, workflow.ArtifactDraft{Kind: "report", Name: "r", Body: "the body"}); err != nil {
		t.Fatal(err)
	}

	// The pinned workflow YAML: the library version the run names (version 1).
	seedLibraryWorkflow(t, a, pid, "build", `apiVersion: forge.flokkit.io/v1
kind: Workflow
metadata: {name: build}
spec:
  kind: lifecycle
  stages: [{name: work, steps: [{name: do, uses: agent/pi}]}]
`)

	code, body := tv_getJSON(t, h, fmt.Sprintf("/api/v1/runs/%d/trace", rid))
	if code != 200 {
		t.Fatalf("trace: %d", code)
	}
	if body["projectSlug"] != "trc" {
		t.Fatalf("projectSlug: %v", body["projectSlug"])
	}
	stages, _ := body["stages"].([]any)
	if len(stages) != 2 || stages[0] != "work" || stages[1] != "after" {
		t.Fatalf("stages: %v", stages)
	}
	steps, _ := body["steps"].([]any)
	if len(steps) != 2 {
		t.Fatalf("want 2 steps, got %d", len(steps))
	}
	s0 := steps[0].(map[string]any)
	if s0["name"] != "do" || s0["stage"] != "work" {
		t.Fatalf("steps out of creation order: %v", steps)
	}
	events, _ := body["events"].([]any)
	if len(events) < 2 {
		t.Fatalf("workflow receipts and trace events: %v", events)
	}
	foundEntered, foundGuidance := false, false
	var previousID ids.ID
	for _, event := range events {
		row := event.(map[string]any)
		text, _ := row["id"].(string)
		id, err := ids.Parse(text)
		if err != nil || id.Compare(previousID) <= 0 {
			t.Fatalf("events must preserve ascending identity order: %v", events)
		}
		previousID = id
		switch row["kind"] {
		case "stage.entered":
			foundEntered = true
		case "guidance.pinned":
			var pinned struct {
				Hash    string `json:"hash"`
				Imports []struct {
					Name    string `json:"name"`
					Version int    `json:"version"`
				} `json:"imports"`
			}
			payload, marshalErr := json.Marshal(row["payload"])
			if err := json.Unmarshal(payload, &pinned); marshalErr != nil || err != nil || pinned.Hash != expectedHash || len(pinned.Imports) != 1 || pinned.Imports[0].Name != "trace/concise" || pinned.Imports[0].Version != 1 {
				t.Fatalf("guidance pin: payload=%v expectedHash=%q err=%v", row["payload"], expectedHash, err)
			}
			foundGuidance = true
		}
	}
	if !foundEntered || !foundGuidance {
		t.Fatalf("explicit trace events absent: %v", events)
	}
	g, _ := body["guidance"].(map[string]any)
	if g == nil || g["hash"] != expectedHash {
		t.Fatalf("pinned guidance hash not surfaced: %v", body["guidance"])
	}
	gImports, _ := g["imports"].([]any)
	if len(gImports) != 1 || gImports[0].(map[string]any)["name"] != "trace/concise" || int(gImports[0].(map[string]any)["version"].(float64)) != 1 {
		t.Fatalf("pinned guidance imports: %v", body["guidance"])
	}
	arts, _ := body["artifacts"].([]any)
	if len(arts) != 1 {
		t.Fatalf("want 1 artifact, got %d", len(arts))
	}
	wf, _ := body["workflow"].(map[string]any)
	if wf == nil || !strings.Contains(wf["yaml"].(string), "kind: Workflow") {
		t.Fatalf("pinned workflow yaml not recovered: %v", wf["yaml"])
	}
	ch, _ := body["change"].(map[string]any)
	if ch == nil || ch["title"] != "make it blue" {
		t.Fatalf("change: %v", body["change"])
	}
}
