package dependency

import (
	"errors"
	"testing"
)

// Losing catalogue access must retain a read error, rather than return the
// UnknownNeeds category that workflow tests deliberately tolerate.
func TestACatalogueReadFailureIsNotAnAbsentKind(t *testing.T) {
	db := testDB(t)
	c := NewCatalog(db, nil)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	_, _, err := (LocalRuntime{Catalog: c}).Fulfil(t.Context(), []string{"postgres"}, nil)
	var unknown *UnknownNeeds
	if err == nil || errors.As(err, &unknown) {
		t.Fatalf("catalogue read failure became a tolerable absent kind: %v", err)
	}
}
