package wow

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/flokkit/apptown/stock"
)

// The catalog the release ships reads, and every entry is one a person can
// apply: named in the catalog's shape, with a workflow that ships too.
func TestTheShippedCatalogReadsAndEachEntryValidates(t *testing.T) {
	t.Parallel()
	entries, err := ReadStockCatalog(stock.FS)
	if err != nil || len(entries) == 0 {
		t.Fatalf("the shipped catalog: %d entries, %v", len(entries), err)
	}
	shipped, _ := readStockWorkflows(stock.FS)
	workflows := map[string]bool{}
	for _, w := range shipped {
		workflows[w.Name] = true
	}
	for _, e := range entries {
		if !workflows[e.Definition.Workflow] {
			t.Errorf("%s runs %q, which the release does not ship", e.Name, e.Definition.Workflow)
		}
	}
}

// A catalog file that does not read is an error, never an empty catalog,
// and an entry that would not apply is refused by name.
func TestABrokenCatalogFileIsAnError(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{
		"missing":      "",
		"unknown key":  "catalog:\n  - name: uplift/x\n    definition: {workflow: quick-change}\n    colour: red\n",
		"bad name":     "catalog:\n  - name: Bad Name\n    definition: {workflow: quick-change}\n",
		"no workflow":  "catalog:\n  - name: uplift/x\n    definition: {prompt: p}\n",
		"listed twice": "catalog:\n  - name: uplift/x\n    definition: {workflow: quick-change}\n  - name: uplift/x\n    definition: {workflow: quick-change}\n",
	} {
		fsys := fstest.MapFS{}
		if body != "" {
			fsys["catalog.yaml"] = &fstest.MapFile{Data: []byte(body)}
		}
		if entries, err := ReadStockCatalog(fsys); err == nil {
			t.Errorf("%s: read as %+v", name, entries)
		}
	}
	s := New(nil, Dependencies{Stock: fstest.MapFS{}})
	if _, err := s.CatalogChange("uplift/x"); err == nil {
		t.Fatal("a catalog that cannot be read answered as if the entry existed")
	}
	s = New(nil, Dependencies{Stock: fstest.MapFS{"catalog.yaml": {Data: []byte("catalog: []\n")}}})
	if _, err := s.CatalogChange("uplift/x"); !errors.Is(err, ErrCatalogMissing) {
		t.Fatalf("a missing entry: %v", err)
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
	def := CatalogDef{Model: "openrouter/openai/gpt-5.6-luna", TimeoutMinutes: 30}
	c := &CatalogChange{Name: "bump-go", Definition: def}
	raw, _ := json.Marshal(c.RunInputs(map[string]string{"goVersion": "1.27"}))
	k := CatalogKnobs(string(raw))
	if k == nil || k.Model != def.Model || k.TimeoutMinutes != 30 || k.Thinking != "" {
		t.Errorf("knobs not extracted: %+v", k)
	}
	// The entry's name and the release whose catalog.yaml it came from name
	// the exact definition that ran.
	if !strings.Contains(string(raw), `"name":"bump-go"`) || !strings.Contains(string(raw), `"release":"`+CatalogRelease()+`"`) {
		t.Errorf("the inputs name the applied definition and its release: %s", raw)
	}
}
