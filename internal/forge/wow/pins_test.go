package wow

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// A publication pins what the library has at its latest versions, leaves
// out what it lacks, and lets only the platform pack pin system workflows;
// a pack whose contents moved is cut again, once.
func TestPinsAndCutOnPinChange(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	packs, lib, wf := NewPacks(db), NewLibrary(db), NewWorkflows(db)
	if _, err := lib.Create(t.Context(), "lang/go", "Go rules", "lang", "bls"); err != nil {
		t.Fatal(err)
	}
	if err := wf.StockInsert(t.Context(), "build", "the build lane", fmt.Sprintf(laneYAML, "build", "the build lane", "")); err != nil {
		t.Fatal(err)
	}
	if err := wf.StockInsert(t.Context(), "land", "the landing", fmt.Sprintf(laneYAML, "land", "the landing", "  system: true\n")); err != nil {
		t.Fatal(err)
	}
	spec := `{"guidance":["lang/go","missing/block"],"workflows":["build","land","nope"]}`
	pins, err := packs.PinsFor(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if pins != `{"blocks":{"lang/go":1},"workflows":{"build":1}}` {
		t.Fatalf("language pack pins: %s", pins)
	}
	platform, err := packs.PinsFor(t.Context(), `{"platform":true,"guidance":[],"workflows":["land"]}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(platform, `"land":1`) {
		t.Fatalf("the platform pack pins system workflows: %s", platform)
	}
	v, err := packs.Publish(t.Context(), "go", "Go", spec, "stock")
	if err != nil || v != 1 {
		t.Fatalf("publish: %v %d", err, v)
	}
	if recuts, err := packs.CutOnPinChange(t.Context()); err != nil || len(recuts) != 0 {
		t.Fatalf("nothing moved: %v %+v", err, recuts)
	}
	block, _ := lib.ByName(t.Context(), "lang/go")
	if err := lib.Bump(t.Context(), block.ID, "Go rules, revised", "[]", "bls", "lang/go"); err != nil {
		t.Fatal(err)
	}
	recuts, err := packs.CutOnPinChange(t.Context())
	if err != nil || len(recuts) != 1 || recuts[0] != (Recut{Name: "go", Version: 2}) {
		t.Fatalf("moved contents cut a version: %v %+v", err, recuts)
	}
	if recuts, _ := packs.CutOnPinChange(t.Context()); len(recuts) != 0 {
		t.Fatalf("cut once: %+v", recuts)
	}
	rec, _ := packs.ByName(t.Context(), "go")
	have, _ := packs.LatestPins(context.Background(), *rec)
	if !strings.Contains(canonJSON(have), `"lang/go":2`) {
		t.Fatalf("the new publication names the new version: %s", have)
	}
}
