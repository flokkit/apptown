package wow

import (
	"errors"
	"io/fs"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/stock"
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
	rows, err := lib.all(t.Context(), BlockFilter{Query: "café", Tag: "review"})
	if err != nil || len(rows) != 1 || rows[0].Name != "a/one" || rows[0].Bytes != len("Read café carefully") || rows[0].BodyMD != "" || rows[0].Summary != "Read café carefully" {
		t.Fatalf("combined filter and byte count: %+v %v", rows, err)
	}
	// The existing LIKE contract includes SQL wildcards; conversion must not
	// accidentally replace it with a literal Go substring search.
	rows, err = lib.all(t.Context(), BlockFilter{Query: "a/_ne"})
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
	if err := packs.setEnabled(t.Context(), "ordinary", false, "alice"); !errors.Is(err, errLastPack) {
		t.Fatalf("platform counted as offered: %v", err)
	}
	if _, err := packs.Publish(t.Context(), "platform", "invalid", `{"platform":"invalid"}`, ""); err != nil {
		t.Fatal(err)
	}
	if err := packs.setEnabled(t.Context(), "ordinary", false, "alice"); err == nil {
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
	rows, err := lib.all(t.Context(), BlockFilter{})
	if err != nil || len(rows) != 1 {
		t.Fatalf("listing: %v %v", rows, err)
	}
	if rows[0].BodyMD != "" || len(rows[0].Summary) == 0 || len(rows[0].Summary) > 240 {
		t.Fatalf("summary not bounded: %+v", rows[0])
	}
}

// A library row's summary is a plain sentence of the block's opening prose,
// made from the stored body while its structure is still there: code,
// comments and markup never reach the row, and a block that is all headings
// still says what it is.
func TestGuidanceSummaryIsTheOpeningProseInPlainWords(t *testing.T) {
	t.Parallel()
	lib := NewLibrary(testDB(t))
	for _, path := range []string{"guidance/deploy/health-declared.md", "guidance/resource/spec-template.md"} {
		raw, err := fs.ReadFile(stock.FS, path)
		if err != nil {
			t.Fatal(err)
		}
		b, err := parseStockGuidance(string(raw))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := lib.Create(t.Context(), b.Name, b.Body, b.Tags, "alice"); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := lib.all(t.Context(), BlockFilter{})
	if err != nil || len(rows) != 2 {
		t.Fatalf("listing: %+v %v", rows, err)
	}
	summary := map[string]string{}
	for _, r := range rows {
		summary[r.Name] = r.Summary
	}
	health := summary["deploy/health-declared"]
	if !strings.HasPrefix(health, "Every part AppTown serves from your build or image declares its readiness") {
		t.Errorf("health summary lost its opening prose: %q", health)
	}
	// Inline code keeps its backticks so the row can set it as code; the
	// indented YAML, emphasis and comments are gone.
	for _, markup := range []string{"app:", "web:", "**", "<!--", "\n"} {
		if strings.Contains(health, markup) {
			t.Errorf("health summary carries %q: %q", markup, health)
		}
	}
	if spec := summary["resource/spec-template"]; spec == "" || strings.Contains(spec, "#") || strings.Contains(spec, "<!--") {
		t.Errorf("a heading-led block's summary is its heading in plain words: %q", spec)
	}
}
