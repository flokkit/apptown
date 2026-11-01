package wow

import (
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/fault"
)

func TestPacksPublishVersionsAndTheSwitch(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	packs := NewPacks(db)
	lib := NewLibrary(db)
	block, err := lib.Create(t.Context(), "lang/go", "Use Go.", "", "alice")
	if err != nil {
		t.Fatal(err)
	}
	spec := `{"guidance":["lang/go"]}`
	v, err := packs.Publish(t.Context(), "go-small", "small Go service", spec, "stock")
	if err != nil || v != 1 {
		t.Fatalf("first publication: %d %v", v, err)
	}
	// Same contents and pins: words refresh, no new version.
	if v, err = packs.Publish(t.Context(), "go-small", "small Go service, tidied", spec, "stock"); err != nil || v != 1 {
		t.Fatalf("unchanged contents: %d %v", v, err)
	}
	// Pins moved: a new version, because a publication names exact contents.
	if err := lib.Bump(t.Context(), block.ID, "Use Go, revised.", "[]", "alice", "lang/go"); err != nil {
		t.Fatal(err)
	}
	if v, err = packs.Publish(t.Context(), "go-small", "small Go service", spec, "stock"); err != nil || v != 2 {
		t.Fatalf("moved pins: %d %v", v, err)
	}
	if _, err := packs.Publish(t.Context(), "go-small", "mine", spec, ""); !errors.Is(err, errPackOwner) {
		t.Fatalf("another origin republished a stock pack: %v", err)
	}
	pv, err := PackVersionIn(t.Context(), db, "go-small", 1)
	if err != nil || pv.PinsJSON == "" {
		t.Fatalf("a named publication stays readable: %+v %v", pv, err)
	}
	if _, err := PackVersionIn(t.Context(), db, "go-small", 9); err == nil {
		t.Fatal("a version that was never published was read")
	}
	if _, err := PackVersionIn(t.Context(), db, "nobody", 1); !errors.Is(err, errPackAbsent) {
		t.Fatalf("absence: %v", err)
	}

	// The switch: the last pack that is on stays on.
	if err := packs.setEnabled(t.Context(), "go-small", false, "alice"); !errors.Is(err, errLastPack) {
		t.Fatalf("the last pack was turned off: %v", err)
	}
	if _, err := packs.Publish(t.Context(), "ours", "custom", `{"guidance":[]}`, ""); err != nil {
		t.Fatal(err)
	}
	if err := packs.setEnabled(t.Context(), "go-small", false, "alice"); err != nil {
		t.Fatalf("with another pack on: %v", err)
	}
	for name, enabled := range map[string]bool{"go-small": false, "ours": true} {
		pack, err := packs.ByName(t.Context(), name)
		if err != nil || pack.Enabled != enabled {
			t.Fatalf("pack %s switch: %+v %v", name, pack, err)
		}
	}
	dbtest.Count(t, db, 1, `SELECT COUNT(*) FROM audit_log WHERE kind='pack.enabled' AND subject='go-small' AND actor='alice'`)

	if err := packs.delete(t.Context(), "go-small", "alice"); !errors.Is(err, errStockPack) {
		t.Fatalf("a stock pack was deleted: %v", err)
	}
	if err := packs.delete(t.Context(), "ours", "alice"); err != nil {
		t.Fatal(err)
	}
	// Absence is the owner's answer, not the store's: a caller reads a
	// not-found refusal without knowing what sql.ErrNoRows is.
	if err := packs.delete(t.Context(), "ours", "alice"); !errors.Is(err, errNoPack) || fault.KindOf(err) != fault.NotFound || errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleting twice: %v", err)
	}
	if err := packs.setEnabled(t.Context(), "nobody", true, "alice"); !errors.Is(err, errNoPack) || fault.KindOf(err) != fault.NotFound {
		t.Fatalf("switching an unknown pack: %v", err)
	}
}

// A stored spec that is not JSON is an error naming the pack, never a pack
// read as having no guidance, workflows or policies.
func TestAPackWhoseSpecCannotBeReadIsAnError(t *testing.T) {
	t.Parallel()
	if _, err := (packRow{Name: "broken", SpecJSON: `{"guidance": "not a list"}`}).pack(); err == nil || !strings.Contains(err.Error(), "broken") {
		t.Fatalf("a corrupt spec read as: %v", err)
	}
	if pk, err := (packRow{Name: "fine", SpecJSON: `{"guidance":["a"]}`}).pack(); err != nil || len(pk.Guidance) != 1 {
		t.Fatalf("a good spec: %+v %v", pk, err)
	}
}
