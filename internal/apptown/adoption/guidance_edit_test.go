package adoption

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/wow"
)

// Two edits of a project's guidance from one reading: exactly one is saved
// with the revision it makes, the other is refused and writes nothing. An
// edit that names no revision is refused.
func TestGuidanceEditsFromOneRevisionOneWins(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	s := New(db, Dependencies{Ways: wow.New(db, wow.Dependencies{})})
	lib := wow.NewLibrary(db)
	pid := adoptionTestProject(t, db, "g", "G")
	a, _ := lib.Create(t.Context(), "lang/go", "go", "", "alice")
	b, _ := lib.Create(t.Context(), "prose/plain", "plain", "", "alice")
	lists := [][]wow.ResolvedPin{
		{{BlockID: a.ID, Name: a.Name, Version: 1}},
		{{BlockID: b.ID, Name: b.Name, Version: 1}},
	}
	if _, err := s.ReplaceGuidance(t.Context(), pid, nil, lists[0], "edit", "alice"); !errors.Is(err, errGuidanceRevisionRequired) {
		t.Fatalf("edit without a revision: %v", err)
	}
	start := int64(0)
	if current, err := s.Current(t.Context(), pid); err == nil && current != nil {
		start = current.Revision
	}
	errs := make([]error, len(lists))
	var wg sync.WaitGroup
	for i, pins := range lists {
		wg.Go(func() { _, errs[i] = s.ReplaceGuidance(t.Context(), pid, &start, pins, "edit", "alice") })
	}
	wg.Wait()
	won := -1
	for i, err := range errs {
		switch {
		case err == nil:
			won = i
		case !errors.Is(err, errGuidanceMoved):
			t.Fatal(err)
		}
	}
	if won < 0 || errs[1-won] == nil {
		t.Fatalf("want one saved and one refused: %v", errs)
	}
	imports, err := s.Imports(t.Context(), pid)
	if err != nil || len(imports) != 1 || imports[0].Name != lists[won][0].Name {
		t.Fatalf("the refused edit leaked: %+v %v", imports, err)
	}
	if current, err := s.Current(t.Context(), pid); err != nil || current.Revision != start+1 {
		t.Fatalf("revision after one save: %+v %v", current, err)
	}
}

// Advancing to the latest moves every guidance pin to its block's newest
// version, keeping order and disabled choices, and the system workflow the
// receipt pinned to its newest version, as one attributed revision; a
// second advance records nothing new.
func TestAdvanceToLatestMovesEveryPinAndKeepsTheChoices(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	ways := wow.New(db, wow.Dependencies{})
	s := New(db, Dependencies{Ways: ways})
	lib := wow.NewLibrary(db)
	pid := adoptionTestProject(t, db, "latest", "Latest")
	a, _ := lib.Create(t.Context(), "lang/go", "go v1", "", "alice")
	b, _ := lib.Create(t.Context(), "prose/plain", "plain v1", "", "alice")
	system := fmt.Sprintf(laneYAML, "system-lane", "system", "  system: true\n")
	if _, _, err := ways.PublishWorkflow(t.Context(), system, "alice"); err != nil {
		t.Fatal(err)
	}
	start := int64(0)
	if current, err := s.Current(t.Context(), pid); err == nil && current != nil {
		start = current.Revision
	}
	pins := []wow.ResolvedPin{{BlockID: b.ID, Name: b.Name, Version: 1, Disabled: true}, {BlockID: a.ID, Name: a.Name, Version: 1}}
	before, err := s.ReplaceGuidance(t.Context(), pid, &start, pins, "edit", "alice")
	if err != nil {
		t.Fatal(err)
	}
	for _, blk := range []*wow.Block{a, b} {
		if err := lib.Bump(t.Context(), blk.ID, blk.Name+" v2", "", "alice", blk.Name); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := ways.PublishWorkflow(t.Context(), system+"# v2\n", "alice"); err != nil {
		t.Fatal(err)
	}
	after, err := s.AdvanceToLatest(t.Context(), pid, "upgrade to 1.0", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if after.Revision != before.Revision+1 || after.CreatedBy != "admin" || after.Reason != "upgrade to 1.0" {
		t.Fatalf("revision %+v after %d, want the next one by admin", after, before.Revision)
	}
	g := after.Receipt.Guidance
	if len(g) != 2 || g[0].Name != "prose/plain" || !g[0].Disabled || g[0].Version != 2 || g[1].Name != "lang/go" || g[1].Disabled || g[1].Version != 2 {
		t.Fatalf("guidance %+v, want both at v2 in the same order and choices", g)
	}
	if w := after.Receipt.Workflows; len(w) != 1 || w[0].Name != "system-lane" || w[0].Version != 2 {
		t.Fatalf("workflows %+v, want system-lane at v2", w)
	}
	again, err := s.AdvanceToLatest(t.Context(), pid, "upgrade to 1.0", "admin")
	if err != nil || again.Revision != after.Revision {
		t.Fatalf("second advance: %+v %v, want revision %d unchanged", again, err, after.Revision)
	}
}
