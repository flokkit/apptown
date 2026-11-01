package wow

import (
	"errors"
	"maps"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/strs"
	"github.com/flokkit/apptown/stock"
)

// A pack names only lines the tool versions list has, and a service it
// proposes names an image of that line: a version is published only when
// both hold, whoever publishes it. With no list, no line is admitted.
//
// Kills: an unknown line published; a service tag on another line
// published; a missing list read as "anything goes".
func TestAPackNamesOnlyToolLinesTheListHas(t *testing.T) {
	t.Parallel()
	s := New(testDB(t), Dependencies{ToolLines: stock.Versions()})
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
		if _, err := s.packs.Publish(t.Context(), "bad-"+strings.ReplaceAll(name, " ", "-"), "", mustJSON(spec), ""); !errors.Is(err, errUnknownToolLine) {
			t.Errorf("%s: %v", name, err)
		}
	}
	none := New(testDB(t), Dependencies{})
	if _, err := none.packs.Publish(t.Context(), "no-list", "", mustJSON(PackSpec{Tools: map[string]string{"node": "24"}}), ""); !errors.Is(err, errUnknownToolLine) {
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
	pubs, err := s.packs.publicationsOf(t.Context(), "node")
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]string{"node": "24"}; !maps.Equal(pubs[0].Spec.Tools, want) {
		t.Fatalf("node's tools %v, want %v", pubs[0].Spec.Tools, want)
	}
}

func mustJSON(v any) string {
	text, err := strs.JSON(v)
	if err != nil {
		panic(err)
	}
	return text
}

// A pack cannot declare a memory default: a part's memory is its
// deployment's override, else its app's declaration, else AppTown's
// default. A pack file naming resources is refused as an unknown key.
func TestAPackDeclaresNoMemoryDefault(t *testing.T) {
	t.Parallel()
	fsys := fstest.MapFS{"packs.yaml": {Data: []byte("packs:\n  - name: small\n    description: a pack\n    resources: {memory: 48M}\n")}}
	if _, err := ReadStockPacks(fsys); err == nil || !strings.Contains(err.Error(), "resources") {
		t.Fatalf("a pack's memory default was read: %v", err)
	}
}

// Packs do not include one another: a stock pack that declares includes is
// refused as an unknown key, so nothing it meant to inherit is silently
// lost. Projects combine packs by taking several.
func TestAPackDeclaresNoInclusion(t *testing.T) {
	t.Parallel()
	fsys := fstest.MapFS{"packs.yaml": {Data: []byte("packs:\n  - name: small\n    description: a pack\n    includes: base\n")}}
	if _, err := ReadStockPacks(fsys); err == nil || !strings.Contains(err.Error(), "includes") {
		t.Fatalf("a pack's inclusion was read: %v", err)
	}
}
