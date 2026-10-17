package conformance

import (
	"database/sql"
	"errors"
	"testing"

	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/forge/adoption"
	"flokkit.io/forge/internal/forge/wow"
)

func TestCollectGuidanceChecks(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	projectID := createTestProject(t, db, "p", "P", "")
	library := wow.NewLibrary(db)
	enabledBlock := wow.StockBlock{Name: "data/sqlite", Body: "body", ChecksJSON: `[{"name":"pure-go","sh":"true","severity":"high"}]`}
	if err := library.StockInsert(t.Context(), enabledBlock); err != nil {
		t.Fatal(err)
	}
	if version, err := library.StockUpdate(t.Context(), enabledBlock); err != nil || version != 2 {
		t.Fatalf("versioned block: %d %v", version, err)
	}
	enabled, err := library.ByName(t.Context(), enabledBlock.Name)
	if err != nil {
		t.Fatal(err)
	}
	if err := library.StockInsert(t.Context(), wow.StockBlock{Name: "lang/style", Body: "body", ChecksJSON: `[{"name":"tone","rubric":"kind"}]`}); err != nil {
		t.Fatal(err)
	}
	disabled, err := library.ByName(t.Context(), "lang/style")
	if err != nil {
		t.Fatal(err)
	}
	if err := adoption.New(db, adoption.Dependencies{}).PutImports(t.Context(), projectID, []wow.ResolvedPin{
		{BlockID: enabled.ID, Name: enabled.Name, Version: 2},
		{BlockID: disabled.ID, Name: disabled.Name, Version: 1, Disabled: true},
	}); err != nil {
		t.Fatal(err)
	}
	service := newServiceWithAuthority(db, testAuthority{}, unavailableHeadSource{testCheckSource{}})
	got, err := service.AdoptedChecks(caller("t"), projectID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 sourced check, got %+v", got)
	}
	sc := got[0]
	if sc.Block != "data/sqlite" || sc.Version != 2 || sc.Check.Name != "pure-go" || sc.Check.Sh != "true" {
		t.Fatalf("sourced check wrong: %+v", sc)
	}
	// A check the project does not adopt now, including one in a disabled
	// block, is a refusal in the owner's words, never the store's absence.
	for _, missing := range [][2]string{{"data/sqlite", "gone"}, {"lang/style", "tone"}} {
		_, err := service.AdoptedCheck(caller("t"), projectID, missing[0], missing[1])
		if !errors.Is(err, ErrCheckNotAdopted) || fault.KindOf(err) != fault.Unprocessable || errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("%s · %s: %v", missing[0], missing[1], err)
		}
	}
}
