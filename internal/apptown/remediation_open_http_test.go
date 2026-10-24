package apptown

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/workflow"
)

func TestRemediationOpenHTTPUsesCurrentAdoptedCheckAndRetainsManualWork(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	alpha, _ := failingFixture(t, a)
	h := a.Handler()
	base := "/api/v1/conformance/remediate"
	for _, tc := range []struct {
		who, body string
		code      int
	}{
		{"t", `{`, 400}, {"outsider", `{"project":"alpha","block":"hygiene","check":"no-fixme"}`, 403},
		{"t", `{"project":"missing"}`, 404}, {"t", `{"project":"alpha","block":"hygiene","check":"no-longer-adopted"}`, 422},
	} {
		if w := authzCall(h, tc.who, "POST", base, tc.body); w.Code != tc.code {
			t.Fatalf("refused remediation: %d %s", w.Code, w.Body.String())
		}
	}
	changes, err := a.Work.Changes(testConformanceContext("t"), alpha, workflow.ChangeList{})
	if err != nil || len(changes) != 0 {
		t.Fatalf("rejection opened changes: %+v %v", changes, err)
	}
	w := authzCall(h, "t", "POST", base, `{"project":" alpha ","block":"hygiene","version":999,"check":"no-fixme","reviewId":"00000000-0001-7000-8000-000000000000","detail":"a.txt still contains FIXME"}`)
	var out struct {
		Change  workflow.Change
		Run     *workflow.Run
		Started bool
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 201 || out.Change.ID.IsZero() || out.Change.ProjectID != alpha || out.Change.Type != "remediation" || out.Started || out.Run != nil || !strings.Contains(out.Change.Body, "a.txt still contains FIXME") {
		t.Fatalf("manual remediation receipt: %d %s %v", w.Code, w.Body.String(), err)
	}
	saved, err := a.Work.Change(testConformanceContext("t"), out.Change.ID)
	if err != nil || saved.Body != out.Change.Body || saved.State != workflow.ChangeInProgress {
		t.Fatalf("manual change not retained: %+v %v", saved, err)
	}
	var acceptance workflow.Acceptance
	if err := json.Unmarshal([]byte(saved.AcceptanceJSON), &acceptance); err != nil || len(acceptance.Checks) != 1 || acceptance.Checks[0].Block != "hygiene" || acceptance.Checks[0].Check != "no-fixme" || acceptance.Checks[0].Version != 1 {
		t.Fatalf("caller changed adopted acceptance: %+v %v", acceptance, err)
	}
}

// A check judged only on the running deployment is skipped at every landing,
// so no change can be accepted on it: opening a remediation for it is
// refused with what to do instead, and opens nothing.
func TestAStandingOnlyCheckIsNotRemediationWork(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	alpha, _ := failingFixture(t, a)
	adopt(t, a, alpha, "memory", 1, checksJSON(`{"appliesTo": ["repository"], "name":"memory-within","evaluator":"runtime","probe":"memory-within","severity":"high","expects":"the deployment runs within its memory"}`))
	w := authzCall(a.Handler(), "t", "POST", "/api/v1/conformance/remediate", `{"project":"alpha","block":"memory","check":"memory-within"}`)
	if w.Code != 422 || !strings.Contains(w.Body.String(), "judged only on the running deployment") {
		t.Fatalf("a standing-only check was opened as remediation work: %d %s", w.Code, w.Body.String())
	}
	if changes, err := a.Work.Changes(testConformanceContext("t"), alpha, workflow.ChangeList{}); err != nil || len(changes) != 0 {
		t.Fatalf("the refusal opened changes: %+v %v", changes, err)
	}
}
