package wow

import (
	"slices"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy"
)

// Packs taken together give a project the union of their setup, with no
// pack above another: every tool, service, policy and extension of each.
//
// Kills: setup read from one pack only (the first pick, as before
// multipack), or a duplicate kept twice.
func TestCombiningPacksUnionsTheirSetup(t *testing.T) {
	t.Parallel()
	policy := PackPolicy{Name: "scan", Rule: "requireStepBefore", With: map[string]any{"requires": "scan", "uses": "deploy"}}
	got := Combine([]Pack{
		{Name: "go", Toolchain: "go", Tools: map[string]string{"go": "1.27"}, Extensions: []string{"lsp"}, Policies: []PackPolicy{policy}},
		{Name: "postgres", Tools: map[string]string{"postgres": "16"}, Services: []deploy.ServiceSpec{{Name: "postgres", Image: "postgres:16"}}, Requires: []string{"scanner"}},
		{Name: "vite-react", Tools: map[string]string{"node": "24"}, Extensions: []string{"lsp"}, Policies: []PackPolicy{policy}},
	}, func(name string) bool { return name == "go" })
	if len(got.Problems) != 0 {
		t.Fatalf("packs that agree have problems: %v", got.Problems)
	}
	if got.Toolchain != "go" || got.Starter != "go" || len(got.Tools) != 3 || got.Tools["node"] != "24" || got.Tools["postgres"] != "16" {
		t.Fatalf("toolchain, starter and tools: %+v", got)
	}
	if len(got.Services) != 1 || len(got.Policies) != 1 || !slices.Equal(got.Extensions, []string{"lsp"}) || !slices.Equal(got.Requires, []string{"scanner"}) {
		t.Fatalf("services, policies, extensions and requirements: %+v", got)
	}
	if !slices.Equal(got.Packs, []string{"go", "postgres", "vite-react"}) {
		t.Fatalf("packs: %v", got.Packs)
	}
}

// Where two packs disagree on something a project has one of, the
// combination is refused and the words name both packs. Order does not
// matter: there is no precedence to resolve it.
//
// Kills: a later pack silently winning a tool line, the toolchain, a
// service or a policy, or two starters both being written.
func TestCombiningPacksThatDisagreeNamesBoth(t *testing.T) {
	t.Parallel()
	starters := func(name string) bool { return name == "a" || name == "b" }
	for what, b := range map[string]Pack{
		"a tool's line": {Name: "b", Tools: map[string]string{"node": "22"}},
		"the toolchain": {Name: "b", Toolchain: "python"},
		"a service":     {Name: "b", Services: []deploy.ServiceSpec{{Name: "postgres", Image: "postgres:15"}}},
		"a policy":      {Name: "b", Policies: []PackPolicy{{Name: "scan", Rule: "requireStepBefore", With: map[string]any{"requires": "lint", "uses": "deploy"}}}},
		"starter files": {Name: "b"},
	} {
		a := Pack{Name: "a", Toolchain: "node", Tools: map[string]string{"node": "24"}, Services: []deploy.ServiceSpec{{Name: "postgres", Image: "postgres:16"}},
			Policies: []PackPolicy{{Name: "scan", Rule: "requireStepBefore", With: map[string]any{"requires": "scan", "uses": "deploy"}}}}
		has := func(name string) bool { return name == "a" }
		if what == "starter files" {
			has = starters
		}
		for _, order := range [][]Pack{{a, b}, {b, a}} {
			got := Combine(order, has)
			if len(got.Problems) != 1 || !strings.Contains(got.Problems[0], "a and b disagree") {
				t.Errorf("%s: problems %v, want one naming a and b", what, got.Problems)
			}
		}
	}
}
