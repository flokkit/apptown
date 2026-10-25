package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/inference"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/model"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/ids"
)

func TestHandleModelSlotsMatrix(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	models := model.New(db)
	projects := project.New(db, nil)
	ways := wow.New(db, wow.Dependencies{})
	handler := NewModelSlots(models, projects, ways, nil, db, nil)

	projectID := httpTestProject(t, db, "demanding", "D")
	if err := projects.SetModelProfile(context.Background(), projectID, "heavy"); err != nil {
		t.Fatal(err)
	}
	// An instance cell on a non-standard profile, plus a project override.
	if err := models.Set(context.Background(), "plan", ids.Nil, "", "heavy", "prov/big", "fixture"); err != nil {
		t.Fatal(err)
	}
	if err := models.Set(context.Background(), "review", projectID, "demanding", "", "prov/proj", "fixture"); err != nil {
		t.Fatal(err)
	}
	// A fallback row and a non-stock ("extra") slot to prove both surface.
	if err := models.SeedFallbacks(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := models.Set(context.Background(), "bespoke", ids.Nil, "", "standard", "prov/x", "fixture"); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/api/v1/model-slots", nil)
	w := httptest.NewRecorder()
	handler.ModelSlots(w, req)
	if w.Code != 200 {
		t.Fatalf("model-slots: %d %s", w.Code, w.Body.String())
	}
	var body struct {
		Profiles  []string
		Slots     []map[string]any
		Projects  []map[string]any
		Overrides []map[string]any
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	// Profiles include standard and the project-declared 'heavy'.
	hasHeavy := false
	for _, p := range body.Profiles {
		if p == "heavy" {
			hasHeavy = true
		}
	}
	if body.Profiles[0] != "standard" || !hasHeavy {
		t.Fatalf("profiles: %v", body.Profiles)
	}
	// The stock slots come first and 'bespoke' (non-stock) is appended.
	byName := map[string]map[string]any{}
	for _, s := range body.Slots {
		byName[s["slot"].(string)] = s
	}
	plan := byName["plan"]
	if plan == nil || plan["gloss"] == "" {
		t.Fatalf("plan slot must carry its gloss: %+v", plan)
	}
	pats := plan["patterns"].(map[string]any)
	if pats["heavy"] != "prov/big" {
		t.Fatalf("plan's heavy cell: %v", pats)
	}
	if byName["bespoke"] == nil {
		t.Fatalf("non-stock configured slot must surface: %v", body.Slots)
	}
	// The project override is reported with its project and slot.
	if len(body.Overrides) != 1 || body.Overrides[0]["project"] != "demanding" || body.Overrides[0]["slot"] != "review" {
		t.Fatalf("overrides: %v", body.Overrides)
	}
	if len(body.Projects) != 1 || body.Projects[0]["profile"] != "heavy" {
		t.Fatalf("projects: %v", body.Projects)
	}
}
func TestHandleModelCatalog(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	bindings := integration.NewWithClock(db, nil)
	handler := NewModelDiscovery(inference.NewCatalog(db, secret.NewService(db, secret.Config{})))

	// A model binding with a default pattern and a curated list; no baseUrl,
	// so no network probe is attempted.
	cfg := `{"model":"anthropic/claude","models":["claude-opus","claude-haiku"]}`
	if _, err := bindings.Save(context.Background(), "anthropic", "model", "anthropic", cfg, "fixture"); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/api/v1/model-catalog", nil)
	w := httptest.NewRecorder()
	handler.ModelCatalog(w, req)
	if w.Code != 200 {
		t.Fatalf("model-catalog: %d %s", w.Code, w.Body.String())
	}
	var body struct {
		Models []map[string]string
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{} // pattern → source
	for _, m := range body.Models {
		got[m["pattern"]] = m["source"]
	}
	if got["anthropic/claude"] != "default" {
		t.Fatalf("the binding's default pattern must be present: %+v", body.Models)
	}
	if got["anthropic/claude-opus"] != "curated" || got["anthropic/claude-haiku"] != "curated" {
		t.Fatalf("curated models must be prefixed with the binding name: %+v", body.Models)
	}
}
