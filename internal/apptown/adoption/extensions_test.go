package adoption

import (
	"testing"

	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestExtensionsSet(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	e := newExtensions(db)
	for _, on := range []bool{true, true} {
		if err := e.Set(t.Context(), idstest.N(3), "lsp", on); err != nil {
			t.Fatal(err)
		}
	}
	if on := extensionsOf(t, db, idstest.N(3)); len(on) != 1 || on[0] != "lsp" {
		t.Fatalf("on twice is one row: %v", on)
	}
	if err := e.Set(t.Context(), idstest.N(3), "lsp", false); err != nil {
		t.Fatal(err)
	}
	if on := extensionsOf(t, db, idstest.N(3)); len(on) != 0 {
		t.Fatalf("off: %v", on)
	}
}
