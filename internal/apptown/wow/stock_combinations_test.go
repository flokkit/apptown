package wow

import (
	"slices"
	"testing"

	"github.com/flokkit/apptown/stock"
)

// stockCombinations is every set of stock packs a project is given: a
// server project takes server-base, one backend, postgres when that
// backend's starter keeps records in it, and a UI pack or none; any other
// pack is taken alone. It is the stock catalogue's own list, as its packs'
// descriptions say, kept here so the tests below judge each one.
func stockCombinations(packs []Pack) [][]string {
	backends := []string{"go", "node", "django", "rails", "java", "dotnet", "rust", "wordpress"}
	paired := map[string]bool{"server-base": true, "postgres": true, "vite-react": true, "server-rendered": true}
	var out [][]string
	for _, b := range backends {
		for _, data := range [][]string{nil, {"postgres"}} {
			if b == "wordpress" && data != nil {
				continue // WordPress keeps its records in its own MariaDB
			}
			for _, ui := range [][]string{nil, {"vite-react"}, {"server-rendered"}} {
				c := append([]string{"server-base", b}, data...)
				out = append(out, append(c, ui...))
			}
		}
	}
	for _, p := range packs {
		if !p.Platform && !paired[p.Name] && !slices.Contains(backends, p.Name) {
			out = append(out, []string{p.Name})
		}
	}
	return out
}

// Every combination the stock catalogue offers can be taken: no two of its
// packs disagree on a tool's line, the toolchain, a service, a policy or
// the starter files, and every pack it names exists. A combination that
// stopped being takeable would be refused to every project offered it.
//
// Kills: a UI pack naming a toolchain (Go plus a Vite UI would conflict),
// a second starter on a pack that is taken with a backend, or a pack the
// list names that the catalogue lost.
func TestEveryStockCombinationCanBeTaken(t *testing.T) {
	t.Parallel()
	packs, err := ReadStockPacks(stock.FS)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Pack{}
	for _, p := range packs {
		byName[p.Name] = p
	}
	starters := (&Service{deps: Dependencies{Stock: stock.FS}}).Seed
	for _, combination := range stockCombinations(packs) {
		var taken []Pack
		for _, name := range combination {
			p, ok := byName[name]
			if !ok {
				t.Fatalf("combination %v names %s, which the stock catalogue does not ship", combination, name)
			}
			taken = append(taken, p)
		}
		if got := Combine(taken, func(name string) bool { return starters(name) != nil }); len(got.Problems) != 0 {
			t.Errorf("combination %v cannot be taken: %v", combination, got.Problems)
		}
	}
}
