package adoption

import (
	"errors"
	"testing"

	"flokkit.io/forge/internal/forge/wow"
)

func TestMissingCatalogRefusesOnlyOperationsThatNeedIt(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	projectID := adoptionTestProject(t, db, "without-ways", "Without ways")
	s := New(db, Dependencies{})
	if text, hash, imports, err := s.Compile(t.Context(), projectID); err != nil || text != "" || hash == "" || len(imports) != 0 {
		t.Fatalf("empty compilation needs no catalog: %q %q %+v %v", text, hash, imports, err)
	}
	if _, err := s.Union(t.Context(), projectID, nil); !errors.Is(err, ErrCatalogUnavailable) {
		t.Fatalf("union without catalog: %v", err)
	}
	catalog := wow.New(db, wow.Dependencies{})
	block, err := catalog.CreateBlock(t.Context(), "plain/rules", "Write plainly.", "", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutImports(t.Context(), projectID, []wow.ResolvedPin{{BlockID: block.ID, Name: block.Name, Version: 1, Disabled: true}}); err != nil {
		t.Fatal(err)
	}
	if text, hash, imports, err := s.Compile(t.Context(), projectID); err != nil || text != "" || hash == "" || len(imports) != 1 {
		t.Fatalf("disabled import needs no catalog: %q %q %+v %v", text, hash, imports, err)
	}
	if err := s.PutImports(t.Context(), projectID, []wow.ResolvedPin{{BlockID: block.ID, Name: block.Name, Version: 1}}); err != nil {
		t.Fatal(err)
	}
	if text, hash, imports, err := s.Compile(t.Context(), projectID); !errors.Is(err, ErrCatalogUnavailable) || text != "" || hash != "" || imports != nil {
		t.Fatalf("enabled import without catalog: %q %q %+v %v", text, hash, imports, err)
	}
}
