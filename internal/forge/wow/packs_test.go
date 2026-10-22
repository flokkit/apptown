package wow

import (
	"database/sql"
	"errors"
	"strings"
	"testing"

	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/fault"
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
	if _, err := packs.Publish(t.Context(), "go-small", "mine", spec, ""); !errors.Is(err, ErrPackOwner) {
		t.Fatalf("another origin republished a stock pack: %v", err)
	}
	pv, err := PackVersionIn(t.Context(), db, "go-small", 1)
	if err != nil || pv.PinsJSON == "" {
		t.Fatalf("a named publication stays readable: %+v %v", pv, err)
	}
	if _, err := PackVersionIn(t.Context(), db, "go-small", 9); err == nil {
		t.Fatal("a version that was never published was read")
	}
	if _, err := PackVersionIn(t.Context(), db, "nobody", 1); !errors.Is(err, ErrPackAbsent) {
		t.Fatalf("absence: %v", err)
	}

	// The switch: the last pack that is on stays on.
	if err := packs.SetEnabled(t.Context(), "go-small", false, "alice"); !errors.Is(err, ErrLastPack) {
		t.Fatalf("the last pack was turned off: %v", err)
	}
	if _, err := packs.Publish(t.Context(), "ours", "custom", `{"guidance":[]}`, ""); err != nil {
		t.Fatal(err)
	}
	if err := packs.SetEnabled(t.Context(), "go-small", false, "alice"); err != nil {
		t.Fatalf("with another pack on: %v", err)
	}
	offered, _ := packs.Offered(t.Context())
	if len(offered) != 1 || offered[0].Name != "ours" {
		t.Fatalf("offered: %+v", offered)
	}
	dbtest.Count(t, db, 1, `SELECT COUNT(*) FROM audit_log WHERE kind='pack.enabled' AND subject='go-small' AND actor='alice'`)

	if err := packs.Delete(t.Context(), "go-small", "alice"); !errors.Is(err, ErrStockPack) {
		t.Fatalf("a stock pack was deleted: %v", err)
	}
	if err := packs.Delete(t.Context(), "ours", "alice"); err != nil {
		t.Fatal(err)
	}
	// Absence is the owner's answer, not the store's: a caller reads a
	// not-found refusal without knowing what sql.ErrNoRows is.
	if err := packs.Delete(t.Context(), "ours", "alice"); !errors.Is(err, ErrNoPack) || fault.KindOf(err) != fault.NotFound || errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleting twice: %v", err)
	}
	if err := packs.SetEnabled(t.Context(), "nobody", true, "alice"); !errors.Is(err, ErrNoPack) || fault.KindOf(err) != fault.NotFound {
		t.Fatalf("switching an unknown pack: %v", err)
	}
}

// A stored spec that is not JSON is an error naming the pack, never a pack
// read as having no guidance, workflows or policies.
func TestAPackWhoseSpecCannotBeReadIsAnError(t *testing.T) {
	t.Parallel()
	if _, err := (PackRecord{Name: "broken", SpecJSON: `{"guidance": "not a list"}`}).Pack(); err == nil || !strings.Contains(err.Error(), "broken") {
		t.Fatalf("a corrupt spec read as: %v", err)
	}
	if pk, err := (PackRecord{Name: "fine", SpecJSON: `{"guidance":["a"]}`}).Pack(); err != nil || len(pk.Guidance) != 1 {
		t.Fatalf("a good spec: %+v %v", pk, err)
	}
}
