package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/ids"
)

func TestSendBackHTTPDeliversFeedbackToTheReturnedWork(t *testing.T) {
	t.Parallel()
	executed := make(chan *workflow.Step, 4)
	runner := func(task *workflow.Execution) {
		step, err := task.Claim()
		if err != nil {
			t.Error(err)
			return
		}
		executed <- step
		if err := task.Finish(workflow.StepOutcome{Status: workflow.StatusFail}, nil); err != nil {
			t.Error(err)
		}
	}
	spec := schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "work", OnEnter: []schema.Step{{Name: "build", Uses: "agent/pi"}}, Steps: []schema.Step{{Name: "accept", Uses: "approval"}}}}}
	f := newWorkflowHTTPFixtureWithSpec(t, true, spec, []string{"agent/pi"}, map[string]workflow.Runner{"agent/pi": runner})
	grantWorkflowHTTPAuthority(t, f)
	f.Work.Wait()
	var initial *workflow.Step
	select {
	case initial = <-executed:
	default:
		t.Fatal("initial agent never executed")
	}
	h := NewChanges(f.Work, f.Projects, f.Access, f.DB, nil)
	call := func(ctx context.Context, id, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/", strings.NewReader(body)).WithContext(ctx)
		r.SetPathValue("id", id)
		r.Header.Set("X-AppTown-Actor", "apptownd-reviewer")
		w := httptest.NewRecorder()
		h.SendBack(w, r)
		return w
	}
	id := fmt.Sprint(f.ChangeID)
	outsider := access.WithCaller(context.Background(), access.Caller{Principal: access.Principal{Kind: "user", Name: "outsider", Via: "session"}, Verified: true})
	for _, tc := range []struct {
		name, id, body string
		ctx            context.Context
		status         int
	}{
		{"malformed ID", "oops", `{"note":"fix it"}`, f.Context, 400},
		{"missing change", "0000000f-423f-7000-8000-000000000000", `{"note":"fix it"}`, f.Context, 404},
		{"foreign project", id, `{"note":"fix it"}`, outsider, 403},
		{"malformed JSON", id, `{"note":`, f.Context, 400},
		{"missing note", id, `{}`, f.Context, 400},
		{"blank note", id, `{"note":" \n "}`, f.Context, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := call(tc.ctx, tc.id, tc.body)
			var response struct{ Error string }
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || w.Code != tc.status || response.Error == "" {
				t.Fatalf("sendback response: %d %s (%v)", w.Code, w.Body.String(), err)
			}
		})
	}
	f.Work.Wait()
	select {
	case step := <-executed:
		t.Fatalf("rejected request dispatched work: %+v", step)
	default:
	}
	before, err := audit.Recent(f.Context, f.DB, audit.Filter{ProjectID: f.ProjectID, KindPrefix: "change.sendback"})
	if err != nil || len(before) != 0 {
		t.Fatalf("rejected requests recorded a sendback: %+v %v", before, err)
	}
	w := call(f.Context, id, `{"note":"Keep the old controls.\nFix the keyboard input."}`)
	var result struct{ RunID, StepID ids.ID }
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != 202 || result.RunID != f.RunID || result.StepID != initial.ID {
		t.Fatalf("queued work response: %d %s (%v)", w.Code, w.Body.String(), err)
	}
	f.Work.Wait()
	select {
	case step := <-executed:
		var config struct{ Sendback, RetryMode string }
		if err := json.Unmarshal(step.Config, &config); err != nil || step.ID != result.StepID || step.RunID != result.RunID || config.Sendback != "Keep the old controls.\nFix the keyboard input." || config.RetryMode != "continue" {
			t.Fatalf("response or feedback disagrees with executed work: %+v %+v (%v)", step, config, err)
		}
	default:
		t.Fatal("accepted sendback never reached its runner")
	}
	receipts, err := audit.Recent(f.Context, f.DB, audit.Filter{ProjectID: f.ProjectID, KindPrefix: "change.sendback"})
	if err != nil || len(receipts) != 1 || receipts[0].Actor != "bls" {
		t.Fatalf("sendback attribution: %+v %v", receipts, err)
	}
	if _, err := f.Work.Abandon(f.Context, workflow.StopWork{ChangeID: f.ChangeID, Reason: "withdrawn"}); err != nil {
		t.Fatal(err)
	}
	w = call(f.Context, id, `{"note":"do not revive closed work"}`)
	if w.Code != 409 {
		t.Fatalf("closed change: %d %s", w.Code, w.Body.String())
	}
	f.Work.Wait()
	select {
	case step := <-executed:
		t.Fatalf("closed change resumed: %+v", step)
	default:
	}
}

func TestSendBackHTTPExplainsMissingWorkingRun(t *testing.T) {
	t.Parallel()
	f := newWorkflowHTTPFixture(t, false)
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"note":"fix the controls"}`)).WithContext(f.Context)
	r.SetPathValue("id", fmt.Sprint(f.ChangeID))
	w := httptest.NewRecorder()
	NewChanges(f.Work, f.Projects, f.Access, f.DB, nil).SendBack(w, r)
	if w.Code != 422 || !strings.Contains(w.Body.String(), "no working run") {
		t.Fatalf("runless change: %d %s", w.Code, w.Body.String())
	}
	runs, err := f.Work.ChangeRuns(f.Context, f.ChangeID, workflow.ChangeRuns{})
	if err != nil || len(runs) != 0 {
		t.Fatalf("unavailable sendback created work: %+v %v", runs, err)
	}
}
