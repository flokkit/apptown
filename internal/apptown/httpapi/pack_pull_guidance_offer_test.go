package httpapi

import (
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// Pull offers a newly published guidance block for a person to add, while
// leaving the project's current guidance imports untouched.
func TestPackPullListsNewGuidanceWithoutAdoptingIt(t *testing.T) {
	t.Parallel()
	f := newPackPullFixture(t)
	f.ways.SyncStock(t.Context())
	pack, err := f.ways.Pack(t.Context(), "go")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := f.adoption.PackPullPlan(t.Context(), f.projectID, *pack)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(plan.Guidance, "lang/go/formatting") {
		t.Fatalf("the pack's blocks the project lacks are in the plan: %v", plan.Guidance)
	}
	req := httptest.NewRequest("POST", "/api/v1/projects/p/pack/pull", strings.NewReader(`{"pack":"go"}`)).WithContext(f.ctx)
	req.SetPathValue("slug", "p")
	w := httptest.NewRecorder()
	NewPackPull(f.adoption, f.projects, f.identity, f.ways, f.db, nil).PackPull(w, req)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"guidanceToAdd":[`) || !strings.Contains(w.Body.String(), `"lang/go/formatting"`) {
		t.Fatalf("the pull reports it for a person: %d %s", w.Code, w.Body.String())
	}
	if n := importedBlocks(t, f.adoption, f.projectID, "lang/go/formatting"); n != 0 {
		t.Fatalf("not adopted by the pull, got %d", n)
	}
	plan, err = f.adoption.PackPullPlan(req.Context(), f.projectID, *pack)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(plan.Guidance, "lang/go/formatting") {
		t.Fatalf("still listed until a person adds it: %v", plan.Guidance)
	}
}
