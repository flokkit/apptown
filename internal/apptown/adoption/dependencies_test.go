package adoption

import (
	"errors"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/wow"
)

func TestMissingCatalogRefusesOnlyOperationsThatNeedIt(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	projectID := adoptionTestProject(t, db, "without-ways", "Without ways")
	s := New(db, Dependencies{})
	if c, err := s.Compile(t.Context(), projectID); err != nil || c.Text != "" || c.Hash == "" || len(c.Imports) != 0 {
		t.Fatalf("empty compilation needs no catalog: %q %q %+v %v", c.Text, c.Hash, c.Imports, err)
	}
	if _, err := s.Union(t.Context(), projectID, nil); !errors.Is(err, errCatalogUnavailable) {
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
	if c, err := s.Compile(t.Context(), projectID); err != nil || c.Text != "" || c.Hash == "" || len(c.Imports) != 1 {
		t.Fatalf("disabled import needs no catalog: %q %q %+v %v", c.Text, c.Hash, c.Imports, err)
	}
	if err := s.PutImports(t.Context(), projectID, []wow.ResolvedPin{{BlockID: block.ID, Name: block.Name, Version: 1}}); err != nil {
		t.Fatal(err)
	}
	if c, err := s.Compile(t.Context(), projectID); !errors.Is(err, errCatalogUnavailable) || c.Text != "" || c.Hash != "" || c.Imports != nil {
		t.Fatalf("enabled import without catalog: %q %q %+v %v", c.Text, c.Hash, c.Imports, err)
	}
}
