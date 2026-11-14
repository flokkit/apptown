package wow

import (
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/stock"
)

// A standing approval's meaning is what a person consents to. When a stock
// workflow's standing acceptance covers work a model judges (a routine step
// with a rubric, in the workflow or in one it calls), its meaning says so;
// otherwise an approval given for the old, version-compared update keeps
// accepting model-judged work without a new human confirmation.
func TestStockStandingMeaningsNameTheModelJudgement(t *testing.T) {
	t.Parallel()
	items, skipped := readStockWorkflows(stock.FS)
	if skipped != 0 || len(items) == 0 {
		t.Fatalf("stock workflows: %d read, %d skipped", len(items), skipped)
	}
	docs := map[string]*schema.Doc{}
	for _, it := range items {
		doc, err := schema.ParseWorkflow([]byte(it.Content))
		if err != nil {
			t.Fatalf("%s: %v", it.Name, err)
		}
		docs[doc.Metadata.Name] = doc
	}
	judged := func(doc *schema.Doc) bool {
		for _, stage := range doc.Spec.Stages {
			for _, step := range append(append([]schema.Step{}, stage.OnEnter...), stage.Steps...) {
				if rubric, _ := step.With["rubric"].(string); step.Uses == "routine" && rubric != "" {
					return true
				}
			}
		}
		return false
	}
	checked := 0
	for name, doc := range docs {
		covers := judged(doc)
		for _, called := range doc.Calls() {
			if docs[called] != nil && judged(docs[called]) {
				covers = true
			}
		}
		for _, stage := range doc.Spec.Stages {
			for _, step := range append(append([]schema.Step{}, stage.OnEnter...), stage.Steps...) {
				standing, _ := step.With["standing"].(map[string]any)
				means, _ := standing["means"].(string)
				if step.Uses != "approval" || means == "" || !covers {
					continue
				}
				checked++
				if !strings.Contains(means, "AI model") {
					t.Errorf("%s: standing meaning %q does not say an AI model judges the update", name, means)
				}
			}
		}
	}
	if checked < 2 {
		t.Fatalf("checked %d standing meanings; janitor-update and janitor-scan both cover judged updates", checked)
	}
}
