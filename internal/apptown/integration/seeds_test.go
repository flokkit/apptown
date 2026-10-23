package integration

import (
	"context"
	"strings"
	"testing"
)

// The pi-default model binding is seeded once, unmanaged: the row is the
// provider definition, so an edit survives the next boot's seeding (a
// managed seed once wiped it on every restart).
func TestSeedModelBindingIsRecordedOnceAndStaysEditable(t *testing.T) {
	t.Parallel()
	s := NewService(testDB(t), Dependencies{})
	s.SeedModelBinding(t.Context())
	seeded, err := s.Bindings.ByName(t.Context(), "pi-default")
	if err != nil {
		t.Fatal(err)
	}
	if cfg, err := seeded.ModelConfig(); err != nil || seeded.Capability != CapModel || seeded.Managed || cfg.Concurrency != 1 {
		t.Fatalf("the model binding is seeded unmanaged, one step at a time: %+v", seeded)
	}
	if _, err := s.Bindings.Save(context.Background(), "pi-default", CapModel, "pi", `{"model":"anthropic/edited"}`, "fixture"); err != nil {
		t.Fatal(err)
	}
	s.SeedModelBinding(t.Context())
	kept, err := s.Bindings.ByName(t.Context(), "pi-default")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(kept.ConfigJSON, "anthropic/edited") || kept.Managed {
		t.Fatalf("a boot's sync overwrote the edited model binding: %+v", kept)
	}
}
