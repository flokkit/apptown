package wow

import (
	"errors"
	"maps"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/stock"
)

// A pack that includes another inherits each tool's line unless it names
// its own, and clears them all only by saying so.
//
// Kills: tools not inherited, a child's line losing to its parent's, the
// child's map replacing the parent's whole, or clears ignoring tools.
func TestAPackInheritsToolLinesItDoesNotName(t *testing.T) {
	t.Parallel()
	inc := &includedPublication{Pack: "base", Version: 1, Spec: PackSpec{Tools: map[string]string{"node": "24", "postgres": "16"}}}
	got, err := resolveDeclaration(t.Context(), PackSpec{Includes: "base", Tools: map[string]string{"node": "26"}}, inc, fakePins{})
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]string{"node": "26", "postgres": "16"}; !maps.Equal(got.Spec.Tools, want) {
		t.Fatalf("tools %v, want %v", got.Spec.Tools, want)
	}
	if inc.Spec.Tools["node"] != "24" {
		t.Fatal("resolving changed the included publication's tools")
	}
	cleared, err := resolveDeclaration(t.Context(), PackSpec{Includes: "base", Clears: []string{"tools"}}, inc, fakePins{})
	if err != nil || len(cleared.Spec.Tools) != 0 {
		t.Fatalf("cleared tools: %v %v", cleared.Spec.Tools, err)
	}
}

// A pack names only lines the tool versions list has, and a service it
// proposes names an image of that line: a version is published only when
// both hold, whoever publishes it. With no list, no line is admitted.
//
// Kills: an unknown line published; a service tag on another line
// published; a missing list read as "anything goes".
func TestAPackNamesOnlyToolLinesTheListHas(t *testing.T) {
	t.Parallel()
	s := New(testDB(t), Dependencies{ToolLines: stock.Tools()})
	ok := map[string]PackSpec{
		"a known line":               {Tools: map[string]string{"node": "24"}},
		"a service on the line":      {Tools: map[string]string{"postgres": "16"}, Services: []deploy.ServiceSpec{{Name: "postgres", Image: "postgres:16"}}},
		"a service on a patch of it": {Tools: map[string]string{"mariadb": "11.4"}, Services: []deploy.ServiceSpec{{Name: "mariadb", Image: "mariadb:11.4.13"}}},
		"a service the tools omit":   {Services: []deploy.ServiceSpec{{Name: "redis", Image: "redis:7"}}},
	}
	for name, spec := range ok {
		if _, err := s.packs.Publish(t.Context(), "ok-"+strings.ReplaceAll(name, " ", "-"), "", mustJSON(spec), ""); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	bad := map[string]PackSpec{
		"an unknown line":               {Tools: map[string]string{"node": "99"}},
		"an unknown tool":               {Tools: map[string]string{"cobol": "85"}},
		"a service on another line":     {Tools: map[string]string{"postgres": "16"}, Services: []deploy.ServiceSpec{{Name: "postgres", Image: "postgres:15"}}},
		"a service line by prefix only": {Tools: map[string]string{"postgres": "16"}, Services: []deploy.ServiceSpec{{Name: "postgres", Image: "postgres:160"}}},
	}
	for name, spec := range bad {
		if _, err := s.packs.Publish(t.Context(), "bad-"+strings.ReplaceAll(name, " ", "-"), "", mustJSON(spec), ""); !errors.Is(err, ErrUnknownToolLine) {
			t.Errorf("%s: %v", name, err)
		}
	}
	none := New(testDB(t), Dependencies{})
	if _, err := none.packs.Publish(t.Context(), "no-list", "", mustJSON(PackSpec{Tools: map[string]string{"node": "24"}}), ""); !errors.Is(err, ErrUnknownToolLine) {
		t.Errorf("without a list: %v", err)
	}
}

// The stock packs state their lines, and every one is in the stock list:
// the release's packs publish.
func TestTheStockPacksStateTheirLines(t *testing.T) {
	t.Parallel()
	s := stockLibrary(t)
	if c := s.SyncStockPacks(t.Context()); c.Errors != 0 {
		t.Fatalf("stock packs: %+v", c)
	}
	pubs, err := s.packs.PublicationsOf(t.Context(), "node-fullstack")
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]string{"node": "24", "postgres": "16"}; !maps.Equal(pubs[0].Spec.Tools, want) {
		t.Fatalf("node-fullstack's tools %v, want %v", pubs[0].Spec.Tools, want)
	}
}
