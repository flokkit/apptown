package adoption

import (
	"context"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/wow"
)

func TestStockKeysAndVersionedUpdates(t *testing.T) {
	t.Parallel()
	if wow.GuidanceKey("body\n\n", "") != wow.GuidanceKey("body", "[]") {
		t.Fatal("trailing newlines and an empty check list read as the same content")
	}
	if wow.PolicyKey("scanFailOnCeiling", `{"max": "high"}`) != wow.PolicyKey("scanFailOnCeiling", `{"max":"high"}`) {
		t.Fatal("policy options compare canonically")
	}
	db := testDB(t)
	lib := wow.NewLibrary(db)
	ads := newLibrary(db, wow.New(db, wow.Dependencies{}))
	if st, err := lib.StockCurrent(t.Context(), "lang/go"); err != nil || st != nil {
		t.Fatalf("absent reads as nil: %+v %v", st, err)
	}
	if err := lib.StockInsert(t.Context(), wow.StockBlock{Name: "lang/go", Tags: "go", Body: "Go it.\n", Tier: ""}); err != nil {
		t.Fatal(err)
	}
	st, _ := lib.StockCurrent(t.Context(), "lang/go")
	if st == nil || !st.IsStock || st.Content != wow.GuidanceKey("Go it.", "") {
		t.Fatalf("seeded: %+v", st)
	}
	pid := adoptionTestProject(t, db, "s", "S")
	b, _ := lib.ByName(t.Context(), "lang/go")
	if err := ads.putImports(t.Context(), pid, []wow.ResolvedPin{{BlockID: b.ID, Name: b.Name, Version: 1}}); err != nil {
		t.Fatal(err)
	}
	version, err := lib.StockUpdate(t.Context(), wow.StockBlock{Name: "lang/go", Tags: "go,lang", Body: "Go it, again."})
	behind, countErr := ImportersBehindIn(context.Background(), db, b.ID, version)
	if countErr != nil {
		t.Fatal(countErr)
	}
	if err != nil || version != 2 || behind != 1 {
		t.Fatalf("a release cuts v2 and reports the project left behind: v%d behind=%d %v", version, behind, err)
	}
	if n := countOf(importsOf(t, db, pid), func(i Import) bool { return i.Version == 1 }); n != 1 {
		t.Fatalf("a stock release moved the project's pin: %d", n)
	}
	if n := receiptsWhere(t, db, ids.Nil, "guidance.stock.upgraded", func(e audit.Entry) bool { return e.Subject == "lang/go" }); n != 1 {
		t.Fatalf("upgrade receipts: %d", n)
	}
	// The versioned update is one transaction: a receipt that cannot be
	// written leaves the block on the version it had.
	dbtest.FailNextStatement(t, db, "INSERT INTO audit_log")
	if _, err := lib.StockUpdate(t.Context(), wow.StockBlock{Name: "lang/go", Body: "third"}); err == nil {
		t.Fatal("an update without its receipt was reported as done")
	}
	if b, err := lib.ByName(t.Context(), "lang/go"); err != nil || b.LatestVersion != 2 {
		t.Fatalf("update without receipt moved the block: %+v %v", b, err)
	}

	wf := wow.NewWorkflows(db)
	if err := wf.StockInsert(t.Context(), "land", "the landing", "yaml: 1\n"); err != nil {
		t.Fatal(err)
	}
	if v, err := wf.StockUpdate(t.Context(), "land", "the landing, updated", "yaml: 2\n"); err != nil || v != 2 {
		t.Fatalf("stock workflow update: v%d %v", v, err)
	}
	wst, _ := wf.StockCurrent(t.Context(), "land")
	if wst == nil || !wst.IsStock || wst.Content != "yaml: 2\n" {
		t.Fatalf("stock workflow: %+v", wst)
	}
}
