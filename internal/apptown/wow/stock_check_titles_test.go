package wow

import (
	"encoding/json"
	"testing"

	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/stock"
)

// Pages name a check by its title; one without a title shows its key
// (project-notes-format) where every other check reads as a sentence. Every
// shipped check therefore carries a title in its declaration.
func TestEveryStockCheckHasATitle(t *testing.T) {
	t.Parallel()
	blocks, skipped := ReadStockGuidance(stock.FS, nil)
	if skipped != 0 || len(blocks) == 0 {
		t.Fatalf("read %d stock blocks, skipped %d", len(blocks), skipped)
	}
	n := 0
	for _, b := range blocks {
		if b.ChecksJSON == "" {
			continue
		}
		var checks []check.Declaration
		if err := json.Unmarshal([]byte(b.ChecksJSON), &checks); err != nil {
			t.Fatalf("%s: %v", b.Name, err)
		}
		for _, c := range checks {
			n++
			if c.Title == "" {
				t.Errorf("%s: check %s has no title", b.Name, c.Name)
			}
		}
	}
	if n == 0 {
		t.Fatal("no stock checks read")
	}
}
