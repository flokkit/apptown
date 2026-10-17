package adoption

import (
	"errors"
	"sync"
	"testing"

	"flokkit.io/forge/internal/forge/wow"
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
	if _, err := s.ReplaceGuidance(t.Context(), pid, nil, lists[0], "edit", "alice"); !errors.Is(err, ErrGuidanceRevisionRequired) {
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
		case !errors.Is(err, ErrGuidanceMoved):
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
