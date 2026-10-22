package wow

import (
	"testing"

	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/stock"
)

// Every skill and resource a stock workflow's steps name ships as a stock
// block: a name the release does not carry fails the step at run time
// ("block not found"), so product-spec and excavate could not start while
// their templates existed only in one instance's library.
func TestStockWorkflowsNameOnlyShippedBlocks(t *testing.T) {
	t.Parallel()
	blocks, skipped := ReadStockGuidance(stock.FS, nil)
	if skipped != 0 {
		t.Fatalf("%d stock guidance files did not parse", skipped)
	}
	shipped := map[string]bool{}
	for _, b := range blocks {
		shipped[b.Name] = true
	}
	items, skipped := readStockWorkflows(stock.FS)
	if skipped != 0 || len(items) == 0 {
		t.Fatalf("stock workflows: %d read, %d skipped", len(items), skipped)
	}
	named := 0
	for _, it := range items {
		doc, err := schema.ParseWorkflow([]byte(it.Content))
		if err != nil {
			t.Fatalf("%s: %v", it.Name, err)
		}
		for _, stage := range doc.Spec.Stages {
			for _, step := range append(append([]schema.Step{}, stage.OnEnter...), stage.Steps...) {
				for _, key := range []string{"skills", "resources"} {
					list, _ := step.With[key].([]any)
					for _, v := range list {
						name, _ := v.(string)
						named++
						if !shipped[name] {
							t.Errorf("%s · %s · %s names %q, which stock/guidance does not ship", it.Name, stage.Name, step.Name, v)
						}
					}
				}
			}
		}
	}
	if named < 3 {
		t.Fatalf("expected the stock workflows to name their skills and templates, saw %d names", named)
	}
}

// A review reads the report of the step its rejection reruns. Rework and
// retries rerun a stage's last agent step (the newest agent/pi step), so a
// review in that stage must read that step's output: reading an earlier
// step's report reviews the first attempt forever (implement reviewed
// build-app's REPORT.md while every rework rewrote only VERIFY.md).
func TestStockReviewsReadTheReworkedReport(t *testing.T) {
	t.Parallel()
	items, _ := readStockWorkflows(stock.FS)
	reviews := 0
	for _, it := range items {
		doc, err := schema.ParseWorkflow([]byte(it.Content))
		if err != nil {
			t.Fatalf("%s: %v", it.Name, err)
		}
		for _, stage := range doc.Spec.Stages {
			reworked := ""
			for _, step := range stage.OnEnter {
				if step.Uses != "agent/pi" {
					continue
				}
				reworked = ""
				if out, ok := step.With["output"].(map[string]any); ok {
					reworked, _ = out["kind"].(string)
				}
			}
			for _, step := range append(append([]schema.Step{}, stage.OnEnter...), stage.Steps...) {
				of, _ := step.With["of"].(string)
				if step.Uses != "agent/review" || of == "" {
					continue
				}
				reviews++
				if of != reworked {
					t.Errorf("%s · %s · %s reviews %q, but a rejection reruns the step that writes %q", it.Name, stage.Name, step.Name, of, reworked)
				}
			}
		}
	}
	if reviews == 0 {
		t.Fatal("expected stock workflows with reviews of a report")
	}
}
