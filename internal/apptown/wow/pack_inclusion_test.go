package wow

import (
	"context"
	"errors"
	"fmt"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/strs"
)

// fakePins is a library for the pure resolution rule.
type fakePins struct {
	blocks    map[string]int
	workflows map[string]int
	system    map[string]bool
	policies  map[string]bool
}

func (f fakePins) BlockPin(ctx context.Context, name string) (int, bool) {
	v, ok := f.blocks[name]
	return v, ok
}
func (f fakePins) WorkflowPin(ctx context.Context, name string, platform bool) (int, bool, bool) {
	v, ok := f.workflows[name]
	return v, ok && (platform || !f.system[name]), ok
}
func (f fakePins) PolicyPin(ctx context.Context, name string) (PackPolicy, bool) {
	if !f.policies[name] {
		return PackPolicy{}, false
	}
	return PackPolicy{Name: name, Rule: "requireCapabilityBound", With: map[string]any{"capability": name}}, true
}

// Resolution is one rule with one answer: the same declaration and the same
// included publication always resolve to the same contents and pins,
// whatever order Go walks the replacements map in.
func TestPackResolutionIsDeterministic(t *testing.T) {
	t.Parallel()
	lib := fakePins{blocks: map[string]int{"team/a": 3, "team/b": 1, "team/x": 2}, workflows: map[string]int{"team-flow": 4}, policies: map[string]bool{"team-strict": true}}
	inc := &includedPublication{Pack: "base", Version: 2,
		Spec: PackSpec{Guidance: []string{"a", "b", "c", "d"}, Workflows: []string{"flow", "land"}, Policies: []PackPolicy{{Name: "strict"}, {Name: "loose"}},
			Resources: appcontract.MemoryDefault{Memory: "160M"}, Toolchain: "go", Extensions: []string{"lsp"}},
		Pins: PinSet{Blocks: map[string]int{"a": 1, "b": 1, "c": 5, "d": 7}, Workflows: map[string]int{"flow": 2}}}
	d := PackSpec{Includes: "base", Replaces: map[string]string{"a": "team/a", "b": "team/b", "flow": "team-flow", "strict": "team-strict"},
		Drops: []PackDrop{{Item: "d", Reason: "not ours"}}, Guidance: []string{"team/x"}, Extensions: []string{"lsp", "dap"}}
	first, err := resolveDeclaration(t.Context(), d, inc, lib)
	if err != nil {
		t.Fatal(err)
	}
	if first.Spec.Resources.Memory != "160M" {
		t.Fatalf("including pack lost default: %+v", first.Spec)
	}
	clearedResources, err := resolveDeclaration(t.Context(), PackSpec{Includes: "base", Clears: []string{"resources"}}, inc, lib)
	if err != nil || clearedResources.Spec.Resources.Memory != "" {
		t.Fatalf("default clear: %+v %v", clearedResources, err)
	}
	want := mustJSON(first.Spec) + mustJSON(first.Pins)
	for range 50 {
		again, err := resolveDeclaration(t.Context(), d, inc, lib)
		if err != nil || mustJSON(again.Spec)+mustJSON(again.Pins) != want {
			t.Fatalf("resolution moved: %v\n%s\n%s", err, mustJSON(again.Spec)+mustJSON(again.Pins), want)
		}
	}
	got := first.Spec
	if strings.Join(got.Guidance, ",") != "team/a,team/b,c,team/x" || strings.Join(got.Workflows, ",") != "team-flow,land" ||
		got.Policies[0].Name != "team-strict" || got.Policies[1].Name != "loose" || len(got.Policies) != 2 {
		t.Fatalf("resolved items: %+v", got)
	}
	if first.Pins.Blocks["c"] != 5 || first.Pins.Blocks["team/a"] != 3 || first.Pins.Blocks["team/x"] != 2 || first.Pins.Workflows["team-flow"] != 4 {
		t.Fatalf("inherited pins come from the included publication, own pins from the library: %+v", first.Pins)
	}
	if _, kept := first.Pins.Blocks["d"]; kept {
		t.Fatalf("a dropped item kept its pin: %+v", first.Pins)
	}
	if got.Toolchain != "go" || strings.Join(got.Extensions, ",") != "lsp,dap" || got.Starter != "base" {
		t.Fatalf("setup: toolchain %q extensions %v starter %q", got.Toolchain, got.Extensions, got.Starter)
	}

	cleared, err := resolveDeclaration(t.Context(), PackSpec{Includes: "base", Clears: []string{"toolchain"}}, inc, lib)
	if err != nil || cleared.Spec.Toolchain != "" {
		t.Fatalf("an explicit clear is an override; an omitted field is inherited: %q %v", cleared.Spec.Toolchain, err)
	}
	for range 20 {
		gone, err := resolveDeclaration(t.Context(), PackSpec{Includes: "base", Drops: []PackDrop{{Item: "zzz"}}, Replaces: map[string]string{"yyy": "team/a", "xxx": "team/b", "www": "team/x"}}, inc, lib)
		if err != nil || strings.Join(gone.Gone, ",") != "zzz,www,xxx,yyy" {
			t.Fatalf("items the included publication lacks are named, in order: %v %v", gone.Gone, err)
		}
	}
	for name, bad := range map[string]PackSpec{
		"an added item already included":    {Includes: "base", Guidance: []string{"c"}},
		"a replacement not in the library":  {Includes: "base", Replaces: map[string]string{"a": "nope"}},
		"an added block not in the library": {Includes: "base", Guidance: []string{"nope"}},
		"clearing what is not inherited":    {Includes: "base", Clears: []string{"guidance"}},
	} {
		if _, err := resolveDeclaration(t.Context(), bad, inc, lib); !errors.Is(err, ErrInclusion) {
			t.Errorf("%s was resolved: %v", name, err)
		}
	}
	platform := *inc
	platform.Spec.Platform = true
	if _, err := resolveDeclaration(t.Context(), PackSpec{Includes: "base"}, &platform, lib); !errors.Is(err, ErrInclusion) {
		t.Errorf("the platform pack was included: %v", err)
	}
}

// An inherited item keeps the included publication's pin when the library
// moves it, through a pinned include and a two-level follow chain alike;
// only the pack's own items are cut again.
func TestInheritedPinsNeverDriftOnLibraryCuts(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	packs, lib, wf := NewPacks(db), NewLibrary(db), NewWorkflows(db)
	for _, b := range []string{"base/a", "base/b", "team/own"} {
		if _, err := lib.Create(t.Context(), b, b+" rules", "", "bls"); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := wf.Put(t.Context(), fmt.Sprintf(laneYAML, "build", "the build lane", ""), "bls"); err != nil {
		t.Fatal(err)
	}
	if _, err := packs.Publish(t.Context(), "base", "Base", `{"guidance":["base/a","base/b"],"workflows":["build"]}`, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := packs.Publish(t.Context(), "team", "Team", `{"includes":"base","guidance":["team/own"]}`, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := packs.Publish(t.Context(), "pinned", "Pinned", `{"includes":"base@1"}`, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := packs.Publish(t.Context(), "org", "Org", `{"includes":"team","drops":[{"item":"base/b","reason":"not here"}]}`, ""); err != nil {
		t.Fatal(err)
	}
	latest := func(name string) Publication {
		t.Helper()
		pubs, err := packs.PublicationsOf(t.Context(), name)
		if err != nil || len(pubs) == 0 {
			t.Fatalf("%s: %v", name, err)
		}
		return pubs[0]
	}
	if org := latest("org"); org.IncludedPack != "team" || org.IncludedVersion != 1 || org.Pins.Blocks["base/a"] != 1 ||
		strings.Join(org.Spec.Guidance, ",") != "base/a,team/own" || org.Declaration.Includes != "team" {
		t.Fatalf("a two-level chain resolves through the recorded publication: %+v", org)
	}

	// The library moves an inherited block and the inherited workflow.
	a, _ := lib.ByName(t.Context(), "base/a")
	if err := lib.Bump(t.Context(), a.ID, "base/a rules, revised", "[]", "bls", "base/a"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := wf.Put(t.Context(), fmt.Sprintf(laneYAML, "build", "the build lane, revised", ""), "bls"); err != nil {
		t.Fatal(err)
	}
	recuts, err := packs.CutOnPinChange(t.Context())
	if err != nil || len(recuts) != 1 || recuts[0].Name != "base" {
		t.Fatalf("only the pack that owns the moved items is cut: %v %+v", err, recuts)
	}
	for _, name := range []string{"team", "pinned", "org"} {
		pub := latest(name)
		if pub.Version != 1 || pub.Pins.Blocks["base/a"] != 1 || pub.Pins.Workflows["build"] != 1 {
			t.Fatalf("%s took a library version its included publication does not name: %+v", name, pub)
		}
	}

	// The pack's own item moves: it is cut, still against base@1.
	own, _ := lib.ByName(t.Context(), "team/own")
	if err := lib.Bump(t.Context(), own.ID, "team rules, revised", "[]", "bls", "team/own"); err != nil {
		t.Fatal(err)
	}
	if recuts, err := packs.CutOnPinChange(t.Context()); err != nil || len(recuts) != 1 || recuts[0] != (Recut{Name: "team", Version: 2}) {
		t.Fatalf("an own item's move cuts the pack: %v %+v", err, recuts)
	}
	if team := latest("team"); team.IncludedVersion != 1 || team.Pins.Blocks["base/a"] != 1 || team.Pins.Blocks["team/own"] != 2 {
		t.Fatalf("the cut kept the included publication: %+v", team)
	}
	if org := latest("org"); org.Version != 1 || org.Pins.Blocks["team/own"] != 1 {
		t.Fatalf("an included pack's new version reached its includer without a person: %+v", org)
	}
}

// Saving a declaration is refused when it cannot be resolved, writing
// nothing: an item it replaces or drops that the included publication
// lacks, itself, a loop, or a pack with no such version.
func TestUnresolvableDeclarationsAreRefused(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	packs, lib := NewPacks(db), NewLibrary(db)
	if _, err := lib.Create(t.Context(), "base/a", "rules", "", "bls"); err != nil {
		t.Fatal(err)
	}
	if _, err := packs.Publish(t.Context(), "base", "Base", `{"guidance":["base/a"]}`, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := packs.Publish(t.Context(), "team", "Team", `{"includes":"base"}`, ""); err != nil {
		t.Fatal(err)
	}
	for name, decl := range map[string]string{
		"a drop of an item base lacks": `{"includes":"base","drops":[{"item":"nope","reason":"x"}]}`,
		"a version base never had":     `{"includes":"base@7"}`,
		"a pack that includes itself":  `{"includes":"base"}`,
		"a loop through team":          `{"includes":"team"}`,
	} {
		target := "base"
		if name == "a drop of an item base lacks" || name == "a version base never had" {
			target = "other"
		}
		if _, err := packs.Publish(t.Context(), target, "x", decl, ""); !errors.Is(err, ErrInclusion) {
			t.Errorf("%s was published: %v", name, err)
		}
	}
	if pubs, _ := packs.PublicationsOf(t.Context(), "base"); len(pubs) != 1 {
		t.Fatalf("a refused declaration wrote a version: %+v", pubs)
	}
	if _, err := packs.ByName(t.Context(), "other"); err == nil {
		t.Fatal("a refused declaration made a pack")
	}
}

// A replacement that names an item the pack already has is refused, for
// blocks, workflows and policies, and two replacements naming one item
// are refused: either would repin an inherited item or list it twice.
func TestAReplacementMayNotNameAnItemAlreadyInThePack(t *testing.T) {
	t.Parallel()
	lib := fakePins{blocks: map[string]int{"base/a": 2, "team/a": 2, "team/b": 1}, workflows: map[string]int{"flow": 3, "other": 3}, policies: map[string]bool{"loose": true}}
	inc := &includedPublication{Pack: "base", Version: 1,
		Spec: PackSpec{Guidance: []string{"base/a", "team/a", "base/b"}, Workflows: []string{"flow", "other"}, Policies: []PackPolicy{{Name: "strict"}, {Name: "loose"}}},
		Pins: PinSet{Blocks: map[string]int{"base/a": 1, "team/a": 1, "base/b": 1}, Workflows: map[string]int{"flow": 1, "other": 1}}}
	for name, replaces := range map[string]map[string]string{
		"a block with an inherited block":      {"base/a": "team/a"},
		"a workflow with an inherited one":     {"flow": "other"},
		"a policy with an inherited one":       {"strict": "loose"},
		"two blocks with one block":            {"base/a": "team/b", "base/b": "team/b"},
		"a block with itself from the library": {"base/a": "base/a"},
	} {
		res, err := resolveDeclaration(t.Context(), PackSpec{Includes: "base", Replaces: replaces}, inc, lib)
		if !errors.Is(err, ErrInclusion) {
			t.Errorf("%s was resolved: %+v %+v", name, res.Spec, res.Pins)
		}
	}
}

// mustJSON encodes a fixture; a fixture that cannot be encoded is a broken test.
func mustJSON(v any) string {
	text, err := strs.JSON(v)
	if err != nil {
		panic(err)
	}
	return text
}
