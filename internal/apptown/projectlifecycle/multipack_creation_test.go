package projectlifecycle

import (
	"context"
	"errors"
	"github.com/flokkit/apptown/internal/apptown/adoption"
	"testing"
)

// Packs that pin a block at different versions are refused before a project
// is made, as adoption would refuse them after.
func TestCreationRefusesDisagreeingGuidancePinsBeforeCreating(t *testing.T) {
	t.Parallel()
	f := newOwnerFixture(t, false)
	b, err := f.ways.CreateBlock(t.Context(), "house-rule", "v1", "", "bls")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.ways.PublishPack(t.Context(), "first", "", `{"guidance":["house-rule"]}`, ""); err != nil {
		t.Fatal(err)
	}
	if err := f.ways.BumpBlock(t.Context(), b.ID, "v2", "", "bls", "house-rule"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ways.PublishPack(t.Context(), "second", "", `{"guidance":["house-rule"]}`, ""); err != nil {
		t.Fatal(err)
	}
	_, err = f.lifecycle.Create(context.Background(), Draft{Slug: "pin-conflict", Name: "Pin conflict", Actor: "bls", Packs: []string{"first", "second"}})
	if !errors.Is(err, adoption.ErrNotAdoptable) || slugTaken(t, f.db, "pin-conflict") {
		t.Fatalf("creation error = %v, project exists = %v; want disagreement refused before creation", err, slugTaken(t, f.db, "pin-conflict"))
	}
}
