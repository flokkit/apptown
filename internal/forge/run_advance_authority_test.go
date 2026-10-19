package forge

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"flokkit.io/forge/internal/forge/httpapi"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/ids/idstest"
)

func TestRunAdvanceHTTPRejectsUnverifiedActor(t *testing.T) {
	t.Parallel()
	a := workAuthorityFixture(t)
	fx := fixtureWork(t, a)
	advanced := eventKind("run.done", "advance.ok", "run.ready")
	eventsBefore := workEventCount(t, a, advanced)
	r := httptest.NewRequest("POST", "/", nil)
	r.SetPathValue("id", fx.Run.String())
	r.Header.Set("X-Forge-Actor", "owner")
	out := httptest.NewRecorder()
	httpapi.NewRuns(a.Work, nil).Advance(out, r.WithContext(callerContext(r)))
	if out.Code != 401 {
		t.Fatalf("spoofed advance %d %s", out.Code, out.Body.String())
	}
	requireEvents(t, a, eventsBefore, advanced)
}

func TestRunAdvanceHTTPReportsBlockingGatesWithoutClaimingCompletion(t *testing.T) {
	t.Parallel()
	a := authzFixture(t)
	fx := fixtureWork(t, a)
	a.Work.Wait()
	h := a.Handler()
	for _, tc := range []struct {
		actor, id string
		status    int
	}{
		{"owner", "bad", 400}, {"owner", idstest.N(999999).String(), 404}, {"outsider", fx.Run.String(), 403},
	} {
		w := authzCall(h, tc.actor, "POST", "/api/v1/runs/"+tc.id+"/advance", `{}`)
		if w.Code != tc.status {
			t.Fatalf("advance rejection: %d %s", w.Code, w.Body.String())
		}
	}
	w := authzCall(h, "owner", "POST", "/api/v1/runs/"+fx.Run.String()+"/advance", `{}`)
	var result workflow.AdvanceResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != 200 || result.Advanced || result.RunStatus != workflow.RunRunning || result.Stage != "apply" || len(result.Blocking) != 2 {
		t.Fatalf("blocked advance: %d %s (%v)", w.Code, w.Body.String(), err)
	}
	for _, step := range result.Blocking {
		if step.RunID != fx.Run || (step.Name != "accept" && step.Name != "gate") {
			t.Fatalf("wrong blockers: %+v", result.Blocking)
		}
	}
	run, err := a.Work.Run(asPerson("owner"), fx.Run)
	if err != nil || run.Status != workflow.RunRunning || run.CurrentStage != "apply" {
		t.Fatalf("blocked run advanced: %+v %v", run, err)
	}
}
