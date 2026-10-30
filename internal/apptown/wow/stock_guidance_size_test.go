package wow

import (
	"testing"

	"github.com/flokkit/apptown/stock"
)

// Every agent step's system prompt carries the rules of each guidance block
// its project adopts (adoption.Resolve), and build steps carry
// skill/implementation on top. These limits hold the stock packs to what
// decides the work; they only go down.
const (
	platformBodyLimit       = 6000
	packBodyLimit           = 23000
	implementationBodyLimit = 7000
)

func TestStockGuidanceFitsItsBudget(t *testing.T) {
	t.Parallel()
	blocks, skipped := ReadStockGuidance(stock.FS, nil)
	if skipped != 0 || len(blocks) == 0 {
		t.Fatalf("read %d stock blocks, skipped %d", len(blocks), skipped)
	}
	// What an agent reads up front: a block's rules and the line naming
	// its skill; the skill itself is loaded only when needed.
	body := map[string]int{}
	for _, b := range blocks {
		rules, skill, err := SplitSkill(b.Name, b.Body)
		if err != nil {
			t.Fatal(err)
		}
		if skill != nil {
			rules += "\n\n" + SkillPointer(*skill)
		}
		body[b.Name] = len(rules)
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
	// A project takes several packs and reads their union, so each
	// combination a project is given is counted, with the platform pack.
	byName := map[string]Pack{}
	for _, p := range packs {
		byName[p.Name] = p
	}
	for _, combination := range stockCombinations(packs) {
		names := map[string]bool{}
		for g := range platform {
			names[g] = true
		}
		for _, name := range combination {
			for _, g := range byName[name].Guidance {
				names[g] = true
			}
		}
		if n := size(names); n > packBodyLimit {
			t.Errorf("packs %v with the platform pack are %d bytes of guidance; the limit is %d", combination, n, packBodyLimit)
		}
	}
	if n := body["skill/implementation"]; n == 0 || n > implementationBodyLimit {
		t.Errorf("skill/implementation is %d bytes; the limit is %d", n, implementationBodyLimit)
	}
}
