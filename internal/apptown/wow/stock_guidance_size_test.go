package wow

import (
	"testing"

	"github.com/flokkit/apptown/stock"
)

// Every agent step's system prompt carries the body of each guidance block
// its project adopts, in full (adoption.Resolve), and build steps carry
// skill/implementation on top. These limits hold the stock packs to what
// decides the work; they only go down.
const (
	platformBodyLimit       = 6000
	packBodyLimit           = 25000
	implementationBodyLimit = 7000
)

func TestStockGuidanceFitsItsBudget(t *testing.T) {
	t.Parallel()
	blocks, skipped := ReadStockGuidance(stock.FS, nil)
	if skipped != 0 || len(blocks) == 0 {
		t.Fatalf("read %d stock blocks, skipped %d", len(blocks), skipped)
	}
	body := map[string]int{}
	for _, b := range blocks {
		body[b.Name] = len(b.Body)
	}
	packs, err := ReadStockPacks(stock.FS)
	if err != nil {
		t.Fatal(err)
	}
	// Every project with a repository is on the platform pack as well as
	// its own, so each pack is counted with it.
	platform := map[string]bool{}
	for _, p := range packs {
		if p.Platform {
			for _, g := range p.Guidance {
				platform[g] = true
			}
		}
	}
	size := func(names map[string]bool) int {
		n := 0
		for name := range names {
			b, ok := body[name]
			if !ok {
				t.Errorf("a pack names %s, which is not a stock block", name)
			}
			n += b
		}
		return n
	}
	if n := size(platform); n > platformBodyLimit {
		t.Errorf("the platform pack's guidance is %d bytes; the limit is %d", n, platformBodyLimit)
	}
	for _, p := range packs {
		if p.Platform {
			continue
		}
		if p.Includes != "" {
			t.Fatalf("pack %s includes %s: count the included pack's guidance here too", p.Name, p.Includes)
		}
		names := map[string]bool{}
		for g := range platform {
			names[g] = true
		}
		for _, g := range p.Guidance {
			names[g] = true
		}
		if n := size(names); n > packBodyLimit {
			t.Errorf("pack %s with the platform pack is %d bytes of guidance; the limit is %d", p.Name, n, packBodyLimit)
		}
	}
	if n := body["skill/implementation"]; n == 0 || n > implementationBodyLimit {
		t.Errorf("skill/implementation is %d bytes; the limit is %d", n, implementationBodyLimit)
	}
}
