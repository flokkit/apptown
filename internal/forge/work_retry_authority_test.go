package forge

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/forge/httpapi"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/ids"
)

// Forge owns the caller binding: a header alone cannot request a retry, while
// an authenticated token carries its actor into the Work receipt and runner.
func TestWorkRetryHTTPUsesVerifiedActorAndRunner(t *testing.T) {
	t.Parallel()
	a := workAuthorityFixture(t)
	a.Work.Stop()
	runID := fixtureWork(t, a).Run
	snapshot, err := a.Work.Snapshot(verifiedContext(context.Background(), access.Principal{Kind: "user", Name: "owner", Via: "token"}), runID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	var stepID ids.ID
	for _, step := range snapshot.Steps {
		if step.Name == "gate" {
			stepID = step.ID
		}
	}
	if stepID.IsZero() {
		t.Fatal("declared failed gate is missing")
	}
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{}`))
	r.SetPathValue("id", stepID.String())
	r.Header.Set("X-Forge-Actor", "owner")
	out := httptest.NewRecorder()
	httpapi.NewRuns(a.Work).StepRun(out, r.WithContext(callerContext(r)))
	if out.Code != 401 {
		t.Fatalf("spoofed actor: %d %s", out.Code, out.Body.String())
	}
	r = r.WithContext(context.WithValue(r.Context(), authCtxKey{}, &authIdentity{User: "owner", Via: "token"}))
	out = httptest.NewRecorder()
	httpapi.NewRuns(a.Work).StepRun(out, r.WithContext(callerContext(r)))
	if out.Code != 202 {
		t.Fatalf("verified retry: %d %s", out.Code, out.Body.String())
	}
	requireEvents(t, a, 1, func(e workflow.Event) bool {
		return e.RunID == runID && e.Kind == "step.retried" && payloadField(e, "by") == "owner"
	})
	if err := a.Work.ExecuteStep(a.asEngine(context.Background()), stepID); err != nil {
		t.Fatal(err)
	}
	failed, err := workflow.ReadStepIn(context.Background(), a.DB, stepID)
	if err != nil || failed.Status != workflow.StatusFail {
		t.Fatalf("retried gate did not fail through its runner: %+v %v", failed, err)
	}
}
