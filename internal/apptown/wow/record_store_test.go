package wow

import (
	"errors"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/dbtest"
)

func TestGuidanceListingCombinesFiltersAndCountsBytes(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	lib := NewLibrary(db)
	for _, b := range []struct{ name, body, tags string }{{"a/one", "Read café carefully", "review"}, {"a/two", "Read café carefully", "reviews"}, {"b/three", "other body", "review"}} {
		if _, err := lib.Create(t.Context(), b.name, b.body, b.tags, "alice"); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := lib.All(t.Context(), BlockFilter{Query: "café", Tag: "review"})
	if err != nil || len(rows) != 1 || rows[0].Name != "a/one" || rows[0].Bytes != len("Read café carefully") || rows[0].BodyMD != "" || rows[0].Summary != "Read café carefully" {
		t.Fatalf("combined filter and byte count: %+v %v", rows, err)
	}
	// The existing LIKE contract includes SQL wildcards; conversion must not
	// accidentally replace it with a literal Go substring search.
	rows, err = lib.All(t.Context(), BlockFilter{Query: "a/_ne"})
	if err != nil || len(rows) != 1 || rows[0].Name != "a/one" {
		t.Fatalf("wildcard search: %+v %v", rows, err)
	}
}

func TestLastPackCheckPreservesStoredBooleanInterpretation(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	packs := NewPacks(db)
	if _, err := packs.Publish(t.Context(), "ordinary", "ordinary", "{}", ""); err != nil {
		t.Fatal(err)
	}
	// PostgreSQL's cast treated this historical representation as platform.
	if _, err := packs.Publish(t.Context(), "platform", "platform", `{"platform":"true"}`, ""); err != nil {
		t.Fatal(err)
	}
	if err := packs.SetEnabled(t.Context(), "ordinary", false, "alice"); !errors.Is(err, ErrLastPack) {
		t.Fatalf("platform counted as offered: %v", err)
	}
	if _, err := packs.Publish(t.Context(), "platform", "invalid", `{"platform":"invalid"}`, ""); err != nil {
		t.Fatal(err)
	}
	if err := packs.SetEnabled(t.Context(), "ordinary", false, "alice"); err == nil {
		t.Fatal("unreadable platform flag admitted disabling the last pack")
	}
	dbtest.Count(t, db, 1, `SELECT COUNT(*) FROM packs WHERE name='ordinary' AND enabled`)
	dbtest.Count(t, db, 0, `SELECT COUNT(*) FROM audit_log WHERE kind='pack.enabled'`)
}

func TestGuidanceSummaryIsBoundedWithoutReturningTheBody(t *testing.T) {
	t.Parallel()
	lib := NewLibrary(testDB(t))
	if _, err := lib.Create(t.Context(), "long/rule", strings.Repeat("word ", 1000), "", "alice"); err != nil {
		t.Fatal(err)
	}
	rows, err := lib.All(t.Context(), BlockFilter{})
	if err != nil || len(rows) != 1 {
		t.Fatalf("listing: %v %v", rows, err)
	}
	if rows[0].BodyMD != "" || len(rows[0].Summary) == 0 || len(rows[0].Summary) > 240 {
		t.Fatalf("summary not bounded: %+v", rows[0])
	}
}
