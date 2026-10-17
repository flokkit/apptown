package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"flokkit.io/forge/internal/forge/wow"
	"flokkit.io/forge/internal/ids"
)

// betweenReads is a steering source whose registry read runs a hook: the
// guidance read calls it after reading the list, where a concurrent save
// once slipped between the list and its revision.
type betweenReads struct{ hook func() }

func (s betweenReads) Registry(ctx context.Context) string         { s.hook(); return "" }
func (s betweenReads) Integrations(context.Context, ids.ID) string { return "" }
func (s betweenReads) Delegation(ctx context.Context) string       { return "" }

// The guidance an editor starts from and its revision are one snapshot: a
// save landing while the page reads cannot pair the old list with the new
// revision, which would let saving the old list pass the guard.
func TestGuidanceListAndRevisionAreReadTogether(t *testing.T) {
	f := newPackPullFixture(t)
	lib := wow.NewLibrary(f.db)
	a, err := lib.Create(t.Context(), "review/old", "old guidance", "", "alice")
	if err != nil {
		t.Fatal(err)
	}
	b, err := lib.Create(t.Context(), "review/new", "new guidance", "", "bob")
	if err != nil {
		t.Fatal(err)
	}
	old := []wow.ResolvedPin{{BlockID: a.ID, Name: a.Name, Version: 1}}
	newer := []wow.ResolvedPin{{BlockID: b.ID, Name: b.Name, Version: 1}}
	zero := int64(0)
	first, err := f.adoption.ReplaceGuidance(t.Context(), f.projectID, &zero, old, "first", "alice")
	if err != nil {
		t.Fatal(err)
	}
	source := betweenReads{hook: func() {
		if _, err := f.adoption.ReplaceGuidance(t.Context(), f.projectID, &first.Revision, newer, "concurrent save", "bob"); err != nil {
			t.Error(err)
		}
	}}
	h := NewGuidance(f.ways, f.adoption, f.projects, nil, NewSteeringInventory(source, source, PlatformSteering{}))
	r := httptest.NewRequest("GET", "/api/v1/projects/p/guidance", nil).WithContext(f.ctx)
	r.SetPathValue("slug", "p")
	w := httptest.NewRecorder()
	h.GetProjectGuidance(w, r)
	if w.Code != 200 {
		t.Fatalf("GET: %d %s", w.Code, w.Body.String())
	}
	var got ProjectGuidance
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Imports) != 1 || got.Imports[0].Name != a.Name || got.Revision != first.Revision {
		t.Fatalf("the list and its revision must be one reading: %+v at %d", got.Imports, got.Revision)
	}
	if _, err := f.adoption.ReplaceGuidance(r.Context(), f.projectID, &got.Revision, old, "save the read list", "alice"); err == nil {
		t.Fatal("a save of the old list overwrote the concurrent edit")
	}
}
