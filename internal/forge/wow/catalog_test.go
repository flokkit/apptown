package wow

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"flokkit.io/forge/internal/dbtest"
)

func TestCatalogVersionsAndPromotionWithReceipts(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	cat := NewCatalog(db)
	def := CatalogDef{Prompt: "Bump Go to {{version}}", Workflow: "quick-change", Params: []CatalogParam{{Name: "version", Required: true}}}
	if _, err := cat.Create(t.Context(), "Bad Name", "", def, "alice"); !errors.Is(err, ErrCatalogName) {
		t.Fatalf("a name outside the shape was accepted: %v", err)
	}
	c, err := cat.Create(t.Context(), "uplift/go-version", "raise the Go version", def, "alice")
	if err != nil || c.State() != CatalogDraft || c.LatestVersion != 1 {
		t.Fatalf("create: %+v %v", c, err)
	}
	if created := wowReceipts(t, db, "catalog.created", "uplift/go-version"); len(created) != 1 || created[0].Kind != "catalog.created" || created[0].Actor != "alice" {
		t.Fatalf("create receipt: %+v", created)
	}

	if _, err := cat.Promote(t.Context(), "uplift/go-version", "  ", "bob"); !errors.Is(err, ErrPromotionNote) {
		t.Fatalf("a promotion without evidence was accepted: %v", err)
	}
	if _, err := cat.Promote(t.Context(), "uplift/nobody", "run 9", "bob"); !errors.Is(err, ErrCatalogMissing) {
		t.Fatalf("a missing change was promoted: %v", err)
	}
	c, err = cat.Promote(t.Context(), "uplift/go-version", "evaluated on run 9", "bob")
	if err != nil || c.State() != Catalogued || c.PromotedVersion != 1 || c.PromotedBy != "bob" {
		t.Fatalf("promote: %+v %v", c, err)
	}
	// A new version is a draft again until promoted; the receipt travels with it.
	def.Prompt = "Bump Go to {{version}}, then run the tests"
	if err := cat.Bump(t.Context(), c.ID, c.Name, def, "alice"); err != nil {
		t.Fatal(err)
	}
	c, _ = cat.ByName(t.Context(), "uplift/go-version")
	got, _ := cat.Definition(t.Context(), c.ID, 2)
	if c.State() != CatalogDraft || c.LatestVersion != 2 || got == nil || got.Prompt != def.Prompt {
		t.Fatalf("bump: %+v %+v", c, got)
	}
	// A promotion whose receipt cannot be written does not promote.
	dbtest.FailNextStatement(t, db, "INSERT INTO audit_log")
	if _, err := cat.Promote(t.Context(), "uplift/go-version", "run 10", "bob"); err == nil {
		t.Fatal("a promotion without its receipt was reported as done")
	}
	if c, err := cat.ByName(t.Context(), "uplift/go-version"); err != nil || c.PromotedVersion != 1 {
		t.Fatalf("a failed promotion moved the promoted version: %+v %v", c, err)
	}
	if receipts := wowReceipts(t, db, "catalog.", ""); len(receipts) != 3 {
		t.Fatalf("catalog receipts: %+v", receipts)
	}
}

func TestCatalogDefRendersItsTemplates(t *testing.T) {
	t.Parallel()
	def := &CatalogDef{
		Workflow: "quick-change",
		Params: []CatalogParam{
			{Name: "goVersion", Required: true},
			{Name: "note", Default: "routine maintenance"},
		},
	}
	tmpl := "Bump Go to {{goVersion}}. Reason: {{note}}."
	out, err := def.Render(tmpl, map[string]string{"goVersion": "1.27"})
	if err != nil || out != "Bump Go to 1.27. Reason: routine maintenance." {
		t.Fatalf("render: %q %v", out, err)
	}
	if _, err := def.Render(tmpl, map[string]string{}); err == nil || !strings.Contains(err.Error(), "required") {
		t.Errorf("missing required param: got %v", err)
	}
	if _, err := def.Render(tmpl, map[string]string{"goVersion": "1.27", "goVerison": "x"}); err == nil || !strings.Contains(err.Error(), "unknown param") {
		t.Errorf("typo param must error, got %v", err)
	}
	if _, err := def.Render("hello {{ghost}}", map[string]string{"goVersion": "1.27"}); err == nil || !strings.Contains(err.Error(), "no param") {
		t.Errorf("orphan placeholder must error, got %v", err)
	}
	if err := (&CatalogDef{}).Validate(); err == nil {
		t.Error("a definition without a workflow is refused")
	}
	if err := (&CatalogDef{Workflow: "w", Params: []CatalogParam{{Name: "a"}, {Name: "a"}}}).Validate(); err == nil {
		t.Error("a duplicate param is refused")
	}
	chore := &CatalogDef{Workflow: "w"}
	if err := chore.Validate(); err != nil || chore.Type != "chore" {
		t.Errorf("an absent type is chore: %v %q", err, chore.Type)
	}
}

func TestCatalogKnobsRoundTrip(t *testing.T) {
	t.Parallel()
	if CatalogKnobs("") != nil || CatalogKnobs("{}") != nil {
		t.Errorf("non-catalog runs must return nil")
	}
	c := &CatalogChange{Name: "bump-go", LatestVersion: 3, PromotedVersion: 3}
	def := &CatalogDef{Model: "openrouter/openai/gpt-5.6-luna", TimeoutMinutes: 30}
	raw, _ := json.Marshal(c.RunInputs(def, map[string]string{"goVersion": "1.27"}))
	k := CatalogKnobs(string(raw))
	if k == nil || k.Model != def.Model || k.TimeoutMinutes != 30 || k.Thinking != "" {
		t.Errorf("knobs not extracted: %+v", k)
	}
	if !strings.Contains(string(raw), `"name":"bump-go@3"`) || !strings.Contains(string(raw), `"status":"catalogued"`) {
		t.Errorf("the inputs name the applied definition: %s", raw)
	}
}
