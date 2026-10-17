package httpapi

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/wow"
	"flokkit.io/forge/internal/ids"
)

// A stock workflow the running release no longer ships is not offered: the
// workflow library leaves it out, and so does the lane list of a project
// that never selected it. A project that selected it still sees it, marked.
//
// Kills: either list offering every stored stock workflow; a selected
// withdrawn lane vanishing from its project or shown unmarked.
func TestOnlyAProjectThatSelectedAWithdrawnWorkflowSeesIt(t *testing.T) {
	t.Parallel()
	f := newPackPullFixture(t)
	f.ways.SyncStock(t.Context())
	// Stored by an earlier release's sync: stock, and not in this release.
	retired := strings.Replace(httpPackWorkflowYAML, "metadata: { name: flow }", "metadata: { name: retired-lane }", 1)
	if err := wow.NewWorkflows(f.db).StockInsert(t.Context(), "retired-lane", "an old lane", retired); err != nil {
		t.Fatal(err)
	}
	if err := f.adoption.SelectLane(f.ctx, f.projectID, "retired-lane", true, "bls"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Transact(t.Context(), f.db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "q", Name: "Q"})
	}); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	NewWorkflowCatalog(f.db, f.ways).LibraryList(w, httptest.NewRequest("GET", "/api/v1/workflows/library", nil).WithContext(f.ctx))
	if w.Code != 200 || strings.Contains(w.Body.String(), `"retired-lane"`) || !strings.Contains(w.Body.String(), `"land"`) {
		t.Fatalf("the library offers the withdrawn workflow, or not the shipped ones: %d %s", w.Code, w.Body.String())
	}

	lanes := func(slug string) map[string]LaneView {
		t.Helper()
		r := httptest.NewRequest("GET", "/api/v1/projects/"+slug+"/lanes", nil).WithContext(f.ctx)
		r.SetPathValue("slug", slug)
		w := httptest.NewRecorder()
		NewProjectWorkflows(f.projects, f.adoption, f.ways, nil, f.db).ProjectLanes(w, r)
		var out ProjectLanes
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil {
			t.Fatalf("lanes of %s: %d %s", slug, w.Code, w.Body.String())
		}
		byName := map[string]LaneView{}
		for _, l := range out.Lanes {
			byName[l.Name] = l
		}
		return byName
	}
	if l, ok := lanes("p")["retired-lane"]; !ok || !l.Selected || !l.Withdrawn {
		t.Fatalf("the project that selected it: %+v %v", l, ok)
	}
	q := lanes("q")
	if _, ok := q["retired-lane"]; ok {
		t.Fatal("a project that never selected the withdrawn workflow is offered it")
	}
	if l, ok := q["quick-change"]; !ok || l.Withdrawn {
		t.Fatalf("a shipped lane is not offered as it was: %+v %v", l, ok)
	}
}
