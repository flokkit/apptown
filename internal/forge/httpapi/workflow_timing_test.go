package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/workflow"
)

func TestWorkflowTimingHTTPFailures(t *testing.T) {
	t.Parallel()
	spec := schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "apply", Steps: []schema.Step{{Name: "accept", Uses: "approval"}}}}}
	f := newWorkflowHTTPFixtureWithSpec(t, true, spec, nil, nil)
	rid := f.RunID
	runs, err := f.Work.Timings(f.Context, workflow.TimingWindow{RunID: &rid})
	if err != nil || len(runs) != 1 {
		t.Fatalf("timing observation: %+v %v", runs, err)
	}
	born := runs[0].CreatedAt
	callRun := func(id string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/api/v1/runs/"+id+"/timing", nil).WithContext(f.Context)
		r.SetPathValue("id", id)
		w := httptest.NewRecorder()
		NewRuns(f.Work).RunTiming(w, r)
		return w
	}
	w := callRun(fmt.Sprint(rid))
	var response struct {
		CreatedAt string
		Steps     []struct {
			Stage, Name, Type, Status string
			Ms, WaitMs, CostMicros    int64
			StartedAt, FinishedAt     string
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || response.CreatedAt != born.Format(time.RFC3339Nano) || len(response.Steps) != 1 {
		t.Fatalf("timeline: %d %s", w.Code, w.Body.String())
	}
	step := response.Steps[0]
	if step.Stage != "apply" || step.Name != "accept" || step.Type != "approval" || step.Status != workflow.StatusPending.String() || step.Ms != 0 || step.WaitMs != 0 || step.CostMicros != 0 || step.StartedAt != "" || step.FinishedAt != "" || strings.Contains(w.Body.String(), "reworks") {
		t.Fatalf("pending approval timeline: %s", w.Body.String())
	}
	for _, id := range []string{"0000000f-423f-7000-8000-000000000000", "invalid"} {
		want := 404
		if id == "invalid" {
			want = 400
		}
		if out := callRun(id); out.Code != want {
			t.Fatalf("id %q: %d", id, out.Code)
		}
	}
	t.Cleanup(f.DB.FailNextStatement("WITH selected AS"))
	if out := callRun(fmt.Sprint(rid)); out.Code != 500 {
		t.Fatalf("run read failure: %d %s", out.Code, out.Body.String())
	}
	t.Cleanup(f.DB.FailNextStatement("WITH selected AS"))
	r := httptest.NewRequest("GET", "/api/v1/timing", nil).WithContext(f.Context)
	w = httptest.NewRecorder()
	NewRuns(f.Work).Timing(w, r)
	if w.Code != 500 {
		t.Fatalf("summary read failure: %d %s", w.Code, w.Body.String())
	}
}

func TestTimingDoorsEnforceVisibilityWithoutRouteGuards(t *testing.T) {
	t.Parallel()
	f := newWorkflowHTTPAuthorityFixture(t)
	for _, who := range []string{"outsider", "owner"} {
		ctx := access.WithCaller(context.Background(), access.Caller{Principal: access.Principal{Kind: "user", Name: who, Via: "token"}, Verified: true})
		r := httptest.NewRequest("GET", fmt.Sprintf("/api/v1/runs/%d/timing", f.RunID), nil).WithContext(ctx)
		r.SetPathValue("id", fmt.Sprint(f.RunID))
		out := httptest.NewRecorder()
		NewRuns(f.Work).RunTiming(out, r)
		want := 403
		if who == "owner" {
			want = 200
		}
		if out.Code != want || strings.Contains(out.Body.String(), `"name":"accept"`) != (who == "owner") {
			t.Fatalf("%s run: %d %s", who, out.Code, out.Body.String())
		}
		out = httptest.NewRecorder()
		NewRuns(f.Work).Timing(out, r)
		var got struct{ Workflows []json.RawMessage }
		if err := json.Unmarshal(out.Body.Bytes(), &got); err != nil || out.Code != 200 {
			t.Fatalf("%s window: %d %s: %v", who, out.Code, out.Body.String(), err)
		}
		expected := 0
		if who == "owner" {
			expected = 1
		}
		if len(got.Workflows) != expected || strings.Contains(out.Body.String(), `"name":"accept"`) != (who == "owner") {
			t.Fatalf("%s visibility: %s", who, out.Body.String())
		}
	}
}
