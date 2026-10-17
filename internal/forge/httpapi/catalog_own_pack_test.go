package httpapi

import (
	"net/http/httptest"
	"strings"
	"testing"

	"flokkit.io/forge/internal/forge/wow"
)

// A foreign pack carrying a missing workflow is information, not a one-click
// invitation to move the project to that pack.
func TestCatalogOffersOnlyOwnPack(t *testing.T) {
	t.Parallel()
	f := newPackPullFixture(t)
	f.publishPack(t, "aaa-wrong-pack", []string{"wanted-lane"})
	f.publishPack(t, "right-pack", []string{"wanted-lane"})
	f.publishPack(t, "empty-pack", nil)
	if err := f.adoption.SetPack(t.Context(), f.projectID, "right-pack"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ways.CreateCatalogChange(t.Context(), "uplift/x", "", wow.CatalogDef{
		Workflow: "wanted-lane", Title: "t", Prompt: "b", Type: "chore",
	}, "bls"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ways.PromoteCatalogChange(t.Context(), "uplift/x", "fixture", "bls"); err != nil {
		t.Fatal(err)
	}
	handler := NewCatalogChanges(f.ways, f.adoption, f.projects, f.identity, nil, "")
	call := func() *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		handler.CatalogList(w, httptest.NewRequest("GET", "/api/v1/catalog?project=p", nil).WithContext(f.ctx))
		return w
	}
	w := call()
	body := w.Body.String()
	if !strings.Contains(body, `"missingWorkflow":"wanted-lane"`) {
		t.Fatalf("missing workflow not flagged: %s", body)
	}
	if !strings.Contains(body, `"fromPack":"right-pack"`) || strings.Contains(body, "aaa-wrong-pack") {
		t.Fatalf("must offer the project's own pack only: %s", body)
	}
	if err := f.adoption.SetPack(t.Context(), f.projectID, "empty-pack"); err != nil {
		t.Fatal(err)
	}
	w = call()
	if strings.Contains(w.Body.String(), `"fromPack"`) {
		t.Fatalf("foreign packs must not be offered: %s", w.Body.String())
	}
}
