package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"flokkit.io/forge/internal/forge/changeactions"
	"flokkit.io/forge/internal/forge/conversation"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/ids"
)

func TestConsoleLandingHTTPRechecksItsFailedGateAndKeepsHumanApproval(t *testing.T) {
	t.Parallel()
	var executions atomic.Int32
	runner := func(task *workflow.Execution) {
		if _, err := task.Claim(); err != nil {
			t.Error(err)
			return
		}
		status := workflow.StatusPass
		if executions.Add(1) == 1 {
			status = workflow.StatusFail
		}
		if err := task.Finish(workflow.StepOutcome{Status: status}, nil); err != nil {
			t.Error(err)
		}
	}
	gate := schema.Step{Name: "accept", Uses: "approval"}
	f := newWorkflowHTTPFixtureWithDefinitions(t, true, map[string]schema.Spec{
		"":     {Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "work", Steps: []schema.Step{gate}}}},
		"land": {Kind: "pipeline", Role: "landing", Stages: []schema.Stage{{Name: "verify", Steps: []schema.Step{{Name: "tests", Uses: "script"}, gate}}}},
	}, []string{"script"}, map[string]workflow.Runner{"script": runner})
	change, err := f.Work.Create(f.Context, workflow.ChangeDraft{ProjectID: f.ProjectID, Title: "Chat work"})
	if err != nil {
		t.Fatal(err)
	}
	sessions, _, session, _ := consoleHTTPFixture(t, map[string]string{conversation.ProjectLabel: fmt.Sprint(f.ProjectID), conversation.SlugLabel: "shop", conversation.ChangeLabel: fmt.Sprint(change.ID)})
	h := NewConsoleChanges(sessions, f.Work, changeactions.New(f.Work, nil, f.DB), f.DB)
	call := func(handler http.HandlerFunc, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/", strings.NewReader(fmt.Sprintf(`{"changeId":%q,"who":"apptownd-operator"}`, f.ChangeID))).WithContext(f.Context)
		r.SetPathValue("sid", session.ID)
		r.Header.Set("X-AppTown-Session-Token", token)
		w := httptest.NewRecorder()
		handler(w, r)
		return w
	}
	if w := call(h.ConsoleLand, "wrong-token"); w.Code != 403 {
		t.Fatalf("unauthenticated landing: %d %s", w.Code, w.Body.String())
	}
	if runs, err := f.Work.ChangeRuns(f.Context, change.ID, workflow.ChangeRuns{}); err != nil || len(runs) != 0 {
		t.Fatalf("denial admitted landing: %+v %v", runs, err)
	}
	if w := call(h.ConsoleRecheck, session.Token); w.Code != 422 || !strings.Contains(w.Body.String(), "no landing in progress") {
		t.Fatalf("recheck before landing: %d %s", w.Code, w.Body.String())
	}
	w := call(h.ConsoleLand, session.Token)
	var landed struct {
		RunID   ids.ID
		Message string
	}
	if err := json.Unmarshal(w.Body.Bytes(), &landed); err != nil || w.Code != 201 || landed.RunID.IsZero() || !strings.Contains(landed.Message, "landing started") {
		t.Fatalf("landing: %d %s (%v)", w.Code, w.Body.String(), err)
	}
	f.Work.Wait()
	snapshot, err := f.Work.Snapshot(f.Context, landed.RunID, workflow.DetailView)
	if err != nil || snapshot.Run.ChangeID != change.ID || snapshot.Run.WorkflowName != "land" || len(snapshot.Steps) != 2 || executions.Load() != 1 {
		t.Fatalf("wrong landing: %+v %v executions=%d", snapshot, err, executions.Load())
	}
	var approval *workflow.Step
	for _, step := range snapshot.Steps {
		if step.Type == "approval" {
			approval = step
		}
		if step.Type == "script" && step.Status != workflow.StatusFail {
			t.Fatalf("gate did not fail: %+v", step)
		}
	}
	if approval == nil || approval.Status != workflow.StatusPending {
		t.Fatalf("human gate missing: %+v", approval)
	}
	if w := call(h.ConsoleLand, session.Token); w.Code != 409 {
		t.Fatalf("duplicate landing: %d %s", w.Code, w.Body.String())
	}
	if w := call(h.ConsoleRecheck, "wrong-token"); w.Code != 403 {
		t.Fatalf("unauthenticated recheck: %d %s", w.Code, w.Body.String())
	}
	f.Work.Wait()
	if executions.Load() != 1 {
		t.Fatal("denied recheck executed a gate")
	}
	w = call(h.ConsoleRecheck, session.Token)
	var recheck struct {
		Rerun   []string
		Message string
	}
	if err := json.Unmarshal(w.Body.Bytes(), &recheck); err != nil || w.Code != 200 || len(recheck.Rerun) != 1 || recheck.Rerun[0] != "tests" || !strings.Contains(recheck.Message, "re-running 1 failed gate") {
		t.Fatalf("recheck: %d %s (%v)", w.Code, w.Body.String(), err)
	}
	f.Work.Wait()
	if executions.Load() != 2 {
		t.Fatalf("accepted recheck never executed: %d", executions.Load())
	}
	snapshot, err = f.Work.Snapshot(f.Context, landed.RunID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	var keptApproval bool
	for _, step := range snapshot.Steps {
		if step.ID == approval.ID {
			keptApproval = true
			if step.Status != approval.Status || !bytes.Equal(step.Evidence, approval.Evidence) {
				t.Fatalf("recheck altered human decision: %+v", step)
			}
		}
		if step.Type == "script" && step.Status != workflow.StatusPass {
			t.Fatalf("retried gate did not settle: %+v", step)
		}
	}
	if !keptApproval {
		t.Fatal("recheck removed the human gate")
	}
	var receipts int
	for _, event := range snapshot.Events {
		if event.Kind != "step.recheck" {
			continue
		}
		receipts++
		var receipt struct{ By, Step string }
		if err := json.Unmarshal(event.Payload, &receipt); err != nil || receipt.By != "bls via console/"+session.ID || receipt.Step != "tests" {
			t.Fatalf("recheck receipt: %s (%v)", event.Payload, err)
		}
	}
	if receipts != 1 {
		t.Fatalf("recheck receipts: %d", receipts)
	}
	w = call(h.ConsoleRecheck, session.Token)
	if err := json.Unmarshal(w.Body.Bytes(), &recheck); err != nil || w.Code != 200 || len(recheck.Rerun) != 0 || !strings.Contains(recheck.Message, "nothing to recheck") {
		t.Fatalf("repeat recheck: %d %s (%v)", w.Code, w.Body.String(), err)
	}
	f.Work.Wait()
	if executions.Load() != 2 {
		t.Fatal("repeat recheck replayed passing gate")
	}
	other, err := f.Work.ChangeRuns(f.Context, f.ChangeID, workflow.ChangeRuns{})
	if err != nil || len(other) != 1 || other[0].ID != f.RunID {
		t.Fatalf("payload selected neighboring work: %+v %v", other, err)
	}
	if _, err := sessions.Get(t.Context(), session.ID); err != nil {
		t.Fatalf("commands ended chat: %v", err)
	}
}

func TestConsoleLandingHTTPProjectChatCannotLandOrRecheck(t *testing.T) {
	t.Parallel()
	f := newWorkflowHTTPFixture(t, true)
	sessions, _, session, _ := consoleHTTPFixture(t, map[string]string{conversation.ProjectLabel: fmt.Sprint(f.ProjectID), conversation.SlugLabel: "shop"})
	h := NewConsoleChanges(sessions, f.Work, changeactions.New(f.Work, nil, f.DB), f.DB)
	for _, handler := range []http.HandlerFunc{h.ConsoleLand, h.ConsoleRecheck} {
		r := httptest.NewRequest("POST", "/", strings.NewReader(fmt.Sprintf(`{"changeId":%q}`, f.ChangeID))).WithContext(f.Context)
		r.SetPathValue("sid", session.ID)
		r.Header.Set("X-AppTown-Session-Token", session.Token)
		w := httptest.NewRecorder()
		handler(w, r)
		if w.Code != 403 || !strings.Contains(w.Body.String(), "only a change chat") {
			t.Fatalf("project chat escaped scope: %d %s", w.Code, w.Body.String())
		}
	}
	runs, err := f.Work.ChangeRuns(f.Context, f.ChangeID, workflow.ChangeRuns{})
	if err != nil || len(runs) != 1 || runs[0].ID != f.RunID {
		t.Fatalf("project chat altered work: %+v %v", runs, err)
	}
}

// A recheck's sentence counts the gates that failed. The catch-up it runs
// again to check the branch as it is now passed, and is not called failed
// (it was, after the one-server test of 22 October).
func TestARechecksSentenceNamesOnlyTheGatesThatFailed(t *testing.T) {
	t.Parallel()
	got := recheckStarted(workflow.LandingRechecked{Names: []string{"catch-up-with-main", "scan-source:gitleaks"}, Failed: []string{"scan-source:gitleaks"}})
	if want := "re-running 1 failed gate(s): scan-source:gitleaks (after catching up with main again) — watch with apptown status"; got.Message != want || len(got.Rerun) != 2 {
		t.Fatalf("recheck answer: %+v, want %q", got, want)
	}
}
