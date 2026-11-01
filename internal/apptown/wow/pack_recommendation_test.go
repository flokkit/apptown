package wow

import (
	"context"
	"errors"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// receipts reads the ledger's receipts of one kind for one pack, newest
// first, through the ledger's own read.
func receipts(t *testing.T, db *database.DB, kind, subject string) []audit.Entry {
	t.Helper()
	got, err := audit.Recent(context.Background(), db, audit.Filter{KindPrefix: kind, Subject: subject})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// A new pack's first version is recommended; saving later versions never
// moves the recommendation, and moving it is one recorded act. A version
// the pack never had is refused and changes nothing.
func TestOnlyRecommendingMovesTheRecommendedVersion(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	packs := NewPacks(db)
	if _, err := packs.Publish(t.Context(), "team", "Team", `{"guidance":[]}`, ""); err != nil {
		t.Fatal(err)
	}
	if v, err := packs.publishNamed(t.Context(), "team", "Team", `{"guidance":[],"toolchain":"go"}`, "", "with go"); err != nil || v != 2 {
		t.Fatalf("second version: %d %v", v, err)
	}
	history, err := packs.history(t.Context(), "team")
	if err != nil || history.Recommended != 1 || history.Latest != 2 || history.Versions[0].Name != "with go" || history.Versions[0].BasedOn != 1 {
		t.Fatalf("a saved version is not recommended, and names its base: %+v %v", history, err)
	}
	if offered, err := packs.offeredContentsOf(t.Context(), "team"); err != nil || offered.Version != 1 || offered.Toolchain != "" {
		t.Fatalf("new projects get the recommended version's contents: %+v %v", offered, err)
	}
	if err := packs.recommend(t.Context(), "team", 9, "alice", ids.Nil); !errors.Is(err, errNoPackVersion) {
		t.Fatalf("a version the pack never had: %v", err)
	}
	if err := packs.recommend(t.Context(), "team", 2, "alice", ids.Nil); err != nil {
		t.Fatal(err)
	}
	if err := packs.recommend(t.Context(), "team", 2, "alice", ids.Nil); err != nil {
		t.Fatal(err)
	}
	if offered, _ := packs.offeredContentsOf(t.Context(), "team"); offered.Version != 2 || offered.Toolchain != "go" {
		t.Fatalf("recommending moves what is offered: %+v", offered)
	}
	if got := receipts(t, db, "pack.recommended", "team"); len(got) != 1 || got[0].Actor != "alice" || canonJSON(string(got[0].Payload)) != `{"from":1,"to":2}` {
		t.Fatalf("one receipt for one move: %+v", got)
	}
	// A recommendation decided on a what-if names it on its receipt.
	if _, err := packs.Publish(t.Context(), "team", "Team", `{"toolchain":"node"}`, ""); err != nil {
		t.Fatal(err)
	}
	if err := packs.recommend(t.Context(), "team", 3, "alice", idstest.N(7)); err != nil {
		t.Fatal(err)
	}
	named := 0
	for _, r := range receipts(t, db, "pack.recommended", "team") {
		if canonJSON(string(r.Payload)) == `{"from":2,"to":3,"whatIf":"`+idstest.N(7).String()+`"}` {
			named++
		}
	}
	if named != 1 {
		t.Fatalf("the receipt names the what-if: %+v", receipts(t, db, "pack.recommended", "team"))
	}
	// Naming a what-if for the version already recommended moves nothing,
	// records nothing and says so.
	if err := packs.recommend(t.Context(), "team", 3, "alice", idstest.N(8)); !errors.Is(err, errAlreadyRecommended) {
		t.Fatalf("a what-if for the recommended version: %v", err)
	}
	if got := receipts(t, db, "pack.recommended", "team"); len(got) != 2 {
		t.Fatalf("a no-op left a receipt: %+v", got)
	}
}

// A release publishes a stock pack's new version without recommending it,
// whether its declaration changed (stock sync) or its blocks moved (the pin
// cut): a what-if can judge it before projects are offered it, and a person
// recommends it. A pack's first version is still its recommended one.
func TestAStockPacksNewVersionWaitsForAPerson(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	packs, lib := NewPacks(db), NewLibrary(db)
	if _, err := lib.Create(t.Context(), "go/a", "go rules", "", "bls"); err != nil {
		t.Fatal(err)
	}
	if _, err := packs.Publish(t.Context(), "go-small", "Go", `{"guidance":["go/a"]}`, "stock"); err != nil {
		t.Fatal(err)
	}
	if first, _ := packs.history(t.Context(), "go-small"); first.Recommended != 1 {
		t.Fatalf("a first version is recommended: %+v", first)
	}
	if _, err := packs.Publish(t.Context(), "go-small", "Go", `{"guidance":["go/a"],"toolchain":"go"}`, "stock"); err != nil {
		t.Fatal(err)
	}
	blk, _ := lib.ByName(t.Context(), "go/a")
	if err := lib.Bump(t.Context(), blk.ID, "go rules, revised", "[]", "bls", "go/a"); err != nil {
		t.Fatal(err)
	}
	if _, err := packs.CutOnPinChange(t.Context()); err != nil {
		t.Fatal(err)
	}
	stock, _ := packs.history(t.Context(), "go-small")
	if stock.Latest != 3 || stock.Recommended != 1 {
		t.Fatalf("stock latest %d recommended %d", stock.Latest, stock.Recommended)
	}
	if got := receipts(t, db, "pack.recommended", "go-small"); len(got) != 0 {
		t.Fatalf("no one recommended it: %+v", got)
	}
	if err := packs.recommend(t.Context(), "go-small", 3, "alice", ids.Nil); err != nil {
		t.Fatal(err)
	}
	if stock, _ := packs.history(t.Context(), "go-small"); stock.Recommended != 3 {
		t.Fatalf("a person's recommendation moves it: %+v", stock)
	}
}
