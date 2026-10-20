package wow

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/clock/clocktest"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
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
	if v, err := packs.PublishNamed(t.Context(), "team", "Team", `{"guidance":[],"toolchain":"go"}`, "", "with go"); err != nil || v != 2 {
		t.Fatalf("second version: %d %v", v, err)
	}
	history, err := packs.History(t.Context(), "team")
	if err != nil || history.Recommended != 1 || history.Latest != 2 || history.Versions[0].Name != "with go" || history.Versions[0].BasedOn != 1 {
		t.Fatalf("a saved version is not recommended, and names its base: %+v %v", history, err)
	}
	if offered, err := packs.OfferedContentsOf(t.Context(), "team"); err != nil || offered.Version != 1 || offered.Toolchain != "" {
		t.Fatalf("new projects get the recommended version's contents: %+v %v", offered, err)
	}
	if err := packs.Recommend(t.Context(), "team", 9, "alice", ids.Nil); !errors.Is(err, ErrNoPackVersion) {
		t.Fatalf("a version the pack never had: %v", err)
	}
	if err := packs.Recommend(t.Context(), "team", 2, "alice", ids.Nil); err != nil {
		t.Fatal(err)
	}
	if err := packs.Recommend(t.Context(), "team", 2, "alice", ids.Nil); err != nil {
		t.Fatal(err)
	}
	if offered, _ := packs.OfferedContentsOf(t.Context(), "team"); offered.Version != 2 || offered.Toolchain != "go" {
		t.Fatalf("recommending moves what is offered: %+v", offered)
	}
	if got := receipts(t, db, "pack.recommended", "team"); len(got) != 1 || got[0].Actor != "alice" || canonJSON(string(got[0].Payload)) != `{"from":1,"to":2}` {
		t.Fatalf("one receipt for one move: %+v", got)
	}
	// A recommendation decided on a what-if names it on its receipt.
	if _, err := packs.Publish(t.Context(), "team", "Team", `{"toolchain":"node"}`, ""); err != nil {
		t.Fatal(err)
	}
	if err := packs.Recommend(t.Context(), "team", 3, "alice", idstest.N(7)); err != nil {
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
	if err := packs.Recommend(t.Context(), "team", 3, "alice", idstest.N(8)); !errors.Is(err, ErrAlreadyRecommended) {
		t.Fatalf("a what-if for the recommended version: %v", err)
	}
	if got := receipts(t, db, "pack.recommended", "team"); len(got) != 2 {
		t.Fatalf("a no-op left a receipt: %+v", got)
	}
}

// When an included pack's recommendation moves, each pack that follows it
// gets a follow version, resolved against the new recommendation and named
// for it, which is not recommended: nothing cascades to the packs that
// include the follower, and a pinned include stays where it is. The note
// names an item the follower replaces that the included pack changed.
func TestFollowVersionsAreCutButNeverRecommended(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	packs, lib := NewPacks(db), NewLibrary(db)
	for _, b := range []string{"base/a", "base/b", "team/a"} {
		if _, err := lib.Create(t.Context(), b, b+" rules", "", "bls"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := packs.Publish(t.Context(), "base", "Base", `{"guidance":["base/a","base/b"]}`, ""); err != nil {
		t.Fatal(err)
	}
	for name, decl := range map[string]string{
		"team":   `{"includes":"base","replaces":{"base/a":"team/a"}}`,
		"pinned": `{"includes":"base@1"}`,
	} {
		if _, err := packs.Publish(t.Context(), name, name, decl, ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := packs.Publish(t.Context(), "org", "Org", `{"includes":"team"}`, ""); err != nil {
		t.Fatal(err)
	}
	a, _ := lib.ByName(t.Context(), "base/a")
	if err := lib.Bump(t.Context(), a.ID, "base/a rules, revised", "[]", "bls", "base/a"); err != nil {
		t.Fatal(err)
	}
	if _, err := packs.CutOnPinChange(t.Context()); err != nil {
		t.Fatal(err)
	}
	if team, _ := packs.History(t.Context(), "team"); team.Latest != 1 {
		t.Fatalf("an unrecommended version of base reached team: %+v", team)
	}
	// A pack saved now includes base's recommended version, not its newest.
	if _, err := packs.Publish(t.Context(), "late", "Late", `{"includes":"base"}`, ""); err != nil {
		t.Fatal(err)
	}
	if late, _ := packs.History(t.Context(), "late"); late.Versions[0].IncludedVersion != 1 {
		t.Fatalf("a new include took an unrecommended version: %+v", late.Versions[0])
	}
	if err := packs.Recommend(t.Context(), "base", 2, "alice", ids.Nil); err != nil {
		t.Fatal(err)
	}
	team, err := packs.History(t.Context(), "team")
	if err != nil || team.Latest != 2 || team.Recommended != 1 {
		t.Fatalf("team follows base v2 without it being recommended: %+v %v", team, err)
	}
	v := team.Versions[0]
	if v.Name != "follows base v2" || v.BasedOn != 1 || v.IncludedPack != "base" || v.IncludedVersion != 2 ||
		!strings.Contains(v.Note, "base v2 changed base/a, which you replace") {
		t.Fatalf("the follow version's lineage and note: %+v", v)
	}
	for _, name := range []string{"pinned", "org"} {
		if h, _ := packs.History(t.Context(), name); h.Latest != 1 {
			t.Fatalf("%s moved without a person: %+v", name, h)
		}
	}
	if got := receipts(t, db, "pack.followed", "team"); len(got) != 1 || got[0].Actor != "alice" {
		t.Fatalf("the follow's receipt: %+v", got)
	}
}

// A follow that cannot be resolved is not cut; its receipt names the
// reason, which the pack's page shows until the pack is published again.
func TestARefusedFollowIsShownUntilThePackMoves(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	packs, lib := NewPacks(db), NewLibrary(db)
	for _, b := range []string{"base/a", "team/x"} {
		if _, err := lib.Create(t.Context(), b, b+" rules", "", "bls"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := packs.Publish(t.Context(), "base", "Base", `{"guidance":["base/a"]}`, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := packs.Publish(t.Context(), "team", "Team", `{"includes":"base","guidance":["team/x"]}`, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := packs.Publish(t.Context(), "base", "Base", `{"guidance":["base/a","team/x"]}`, ""); err != nil {
		t.Fatal(err)
	}
	if err := packs.Recommend(t.Context(), "base", 2, "alice", ids.Nil); err != nil {
		t.Fatal(err)
	}
	team, err := packs.History(t.Context(), "team")
	if err != nil || team.Latest != 1 || !strings.Contains(team.FollowRefused, `"team/x" is already included`) {
		t.Fatalf("a refused follow: %+v %v", team, err)
	}
	if _, err := packs.Publish(t.Context(), "team", "Team", `{"includes":"base"}`, ""); err != nil {
		t.Fatal(err)
	}
	if team, _ := packs.History(t.Context(), "team"); team.FollowRefused != "" || team.Versions[0].IncludedVersion != 2 {
		t.Fatalf("the edit that settles it clears the refusal: %+v", team)
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
	if _, err := packs.Publish(t.Context(), "team", "Team", `{"includes":"go-small"}`, ""); err != nil {
		t.Fatal(err)
	}
	if first, _ := packs.History(t.Context(), "go-small"); first.Recommended != 1 {
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
	stock, _ := packs.History(t.Context(), "go-small")
	team, _ := packs.History(t.Context(), "team")
	if stock.Latest != 3 || stock.Recommended != 1 || team.Latest != 1 || team.Recommended != 1 {
		t.Fatalf("stock latest %d recommended %d, team latest %d recommended %d", stock.Latest, stock.Recommended, team.Latest, team.Recommended)
	}
	if got := receipts(t, db, "pack.recommended", "go-small"); len(got) != 0 {
		t.Fatalf("no one recommended it: %+v", got)
	}
	if err := packs.Recommend(t.Context(), "go-small", 3, "alice", ids.Nil); err != nil {
		t.Fatal(err)
	}
	if stock, _ := packs.History(t.Context(), "go-small"); stock.Recommended != 3 {
		t.Fatalf("a person's recommendation moves it: %+v", stock)
	}
}

// A library sweep that moves a stock pack's block and a follower's own
// block recuts the follower from its own items; when a person then
// recommends the stock pack's new version, the follow starts from that
// recut, never from the version before the sweep.
func TestARecommendationAfterASweepFollowsFromTheRecut(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	packs, lib := NewPacks(db), NewLibrary(db)
	for _, b := range []string{"base/a", "team/own"} {
		if _, err := lib.Create(t.Context(), b, b+" rules", "", "bls"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := packs.Publish(t.Context(), "base", "Base", `{"guidance":["base/a"]}`, "stock"); err != nil {
		t.Fatal(err)
	}
	if _, err := packs.Publish(t.Context(), "team", "Team", `{"includes":"base","guidance":["team/own"]}`, ""); err != nil {
		t.Fatal(err)
	}
	for _, b := range []string{"base/a", "team/own"} {
		blk, _ := lib.ByName(t.Context(), b)
		if err := lib.Bump(t.Context(), blk.ID, b+" rules, revised", "[]", "bls", b); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := packs.CutOnPinChange(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := packs.Recommend(t.Context(), "base", 2, "alice", ids.Nil); err != nil {
		t.Fatal(err)
	}
	team, err := packs.History(t.Context(), "team")
	if err != nil {
		t.Fatal(err)
	}
	newest := team.Versions[0]
	if newest.IncludedVersion != 2 || newest.Pins.Blocks["base/a"] != 2 || newest.Pins.Blocks["team/own"] != 2 {
		t.Fatalf("the follow undid the recut: %+v", team.Versions)
	}
}

// An included pack that starts carrying what a follower replaces one of its
// items with is not followed: the replacement would repin an inherited item
// at the library's version. The refusal is shown, as any refused follow is.
func TestAReplacementThatBecomesInheritedIsNotFollowed(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	packs, lib := NewPacks(db), NewLibrary(db)
	for _, b := range []string{"base/a", "team/a"} {
		if _, err := lib.Create(t.Context(), b, b+" rules", "", "bls"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := packs.Publish(t.Context(), "base", "Base", `{"guidance":["base/a"]}`, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := packs.Publish(t.Context(), "team", "Team", `{"includes":"base","replaces":{"base/a":"team/a"}}`, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := packs.Publish(t.Context(), "base", "Base", `{"guidance":["base/a","team/a"]}`, ""); err != nil {
		t.Fatal(err)
	}
	blk, _ := lib.ByName(t.Context(), "team/a")
	if err := lib.Bump(t.Context(), blk.ID, "team rules, revised", "[]", "bls", "team/a"); err != nil {
		t.Fatal(err)
	}
	if err := packs.Recommend(t.Context(), "base", 2, "alice", ids.Nil); err != nil {
		t.Fatal(err)
	}
	team, err := packs.History(t.Context(), "team")
	if err != nil || team.Latest != 1 || !strings.Contains(team.FollowRefused, `"team/a" is already in the pack`) {
		t.Fatalf("the follow that would repin team/a: %+v %v", team, err)
	}
}

// Recommending a version and the follow versions it cuts are one act at
// one reading of AppTown's clock: the follow version, its receipt and the
// recommendation's receipt carry the same time. On a clock that moves on a
// second at every reading, a separate reading for any of them would say a
// later second.
func TestAFollowVersionAndItsReceiptsCarryTheRecommendationsTime(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	clk := clocktest.NewStepping(time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC), time.Second)
	packs := NewPacks(db)
	packs.clock = clk.Now
	if _, err := packs.Publish(t.Context(), "base", "Base", `{"guidance":[]}`, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := packs.Publish(t.Context(), "team", "Team", `{"includes":"base"}`, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := packs.Publish(t.Context(), "base", "Base", `{"guidance":[],"toolchain":"go"}`, ""); err != nil {
		t.Fatal(err)
	}
	reading := clk.Next()
	if err := packs.Recommend(t.Context(), "base", 2, "alice", ids.Nil); err != nil {
		t.Fatal(err)
	}
	team, err := packs.History(t.Context(), "team")
	if err != nil || team.Latest != 2 {
		t.Fatalf("team's follow version: %+v %v", team, err)
	}
	at := func(kind, subject string) time.Time {
		t.Helper()
		got := receipts(t, db, kind, subject)
		if len(got) != 1 {
			t.Fatalf("%s receipts: %+v", kind, got)
		}
		parsed, err := time.Parse(time.RFC3339Nano, got[0].At)
		if err != nil {
			t.Fatal(err)
		}
		return parsed
	}
	for what, got := range map[string]time.Time{
		"the follow version":           team.Versions[0].At.Time,
		"the follow's receipt":         at("pack.followed", "team"),
		"the recommendation's receipt": at("pack.recommended", "base"),
	} {
		if !got.Equal(reading) {
			t.Errorf("%s at %v, the recommendation's reading %v", what, got, reading)
		}
	}
}
