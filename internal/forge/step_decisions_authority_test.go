package forge

import (
	"testing"

	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/forge/workflow"
)

func TestStepOverrideHTTPUsesVerifiedActorAndReportsFailures(t *testing.T) {
	t.Parallel()
	a := authzFixture(t)
	fx := fixtureWork(t, a)
	a.Work.Stop()
	h := a.Handler()
	if out := authzCall(h, "owner", "POST", "/api/v1/steps/"+fx.Gate.String()+"/override", `{"reason":" "}`); out.Code != 400 {
		t.Fatalf("empty reason: %d %s", out.Code, out.Body.String())
	}
	dbtest.FailNextStatement(t, a.DB, "INSERT INTO run_events")
	if out := authzCall(h, "owner", "POST", "/api/v1/steps/"+fx.Gate.String()+"/override", `{"reason":"reviewed"}`); out.Code != 500 {
		t.Fatalf("failed receipt: %d %s", out.Code, out.Body.String())
	}
	requireSteps(t, a, 1, func(s *workflow.Step) bool {
		return s.ID == fx.Gate && s.Status == workflow.StatusFail && s.OverrideBy == ""
	})
	if out := authzCall(h, "owner", "POST", "/api/v1/steps/"+fx.Gate.String()+"/override", `{"user":"boss","reason":"reviewed"}`); out.Code != 200 {
		t.Fatalf("override: %d %s", out.Code, out.Body.String())
	}
	requireSteps(t, a, 1, func(s *workflow.Step) bool { return s.ID == fx.Gate && s.OverrideBy == "owner" })
	requireEvents(t, a, 1, func(e workflow.Event) bool {
		return e.Kind == "override" && payloadField(e, "by") == "owner"
	})
	if out := authzCall(h, "owner", "POST", "/api/v1/steps/"+fx.Gate.String()+"/override", `{"reason":"again"}`); out.Code != 409 {
		t.Fatalf("duplicate: %d %s", out.Code, out.Body.String())
	}
}
