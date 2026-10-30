package httpapi

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/wow"
)

// A contradictory block is offered only after the conflicting pin is disabled.
func TestPackPullRefusesAConflictingBlock(t *testing.T) {
	t.Parallel()
	f := newPackPullFixture(t)
	f.ways.SyncStock(t.Context())
	networked, err := f.ways.BlockByName(t.Context(), "data/networked-database")
	if err != nil {
		t.Fatal(err)
	}
	if networked.Conflicts != "data/sqlite-everywhere" {
		t.Fatalf("the stock block's conflicts are synced: %q", networked.Conflicts)
	}
	// The stock SQLite block is no longer shipped; a project may still hold
	// a block of that name, which the conflict is by.
	sqlite, err := f.ways.CreateBlock(t.Context(), "data/sqlite-everywhere", "Use SQLite.", "data", "bls")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.adoption.PutImports(t.Context(), f.projectID, []wow.ResolvedPin{{BlockID: sqlite.ID, Name: sqlite.Name, Version: sqlite.LatestVersion}}); err != nil {
		t.Fatal(err)
	}
	pack, err := f.ways.Pack(t.Context(), "postgres")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := f.adoption.PackPullPlan(t.Context(), f.projectID, *pack)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range plan.Guidance {
		if name == "data/networked-database" {
			t.Fatalf("a contradicting block is in the plan's adoptions: %v", plan.Guidance)
		}
	}
	if len(plan.Conflicting) != 1 || !strings.Contains(plan.Conflicting[0], "contradicts data/sqlite-everywhere") {
		t.Fatalf("it is listed for a person to choose: %v", plan.Conflicting)
	}
	handler := NewPackPull(f.adoption, f.projects, f.identity, f.ways, f.db, nil)
	r := httptest.NewRequest("POST", "/api/v1/projects/p/pack/pull", strings.NewReader(`{"pack":"postgres"}`)).WithContext(f.ctx)
	r.SetPathValue("slug", "p")
	w := httptest.NewRecorder()
	handler.PackPull(w, r)
	if w.Code != 200 {
		t.Fatalf("pull: %d %s", w.Code, w.Body.String())
	}
	if n := importedBlocks(t, f.adoption, f.projectID, "data/networked-database"); n != 0 {
		t.Fatalf("not adopted: %d", n)
	}
	if err := f.adoption.PutImports(r.Context(), f.projectID, []wow.ResolvedPin{{BlockID: sqlite.ID, Name: sqlite.Name, Version: sqlite.LatestVersion, Disabled: true}}); err != nil {
		t.Fatal(err)
	}
	plan, err = f.adoption.PackPullPlan(r.Context(), f.projectID, *pack)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, name := range plan.Guidance {
		found = found || name == "data/networked-database"
	}
	if !found || len(plan.Conflicting) != 0 {
		t.Fatalf("once the pin is gone it is offered: %v %v", plan.Guidance, plan.Conflicting)
	}
}
