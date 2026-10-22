package integration

import (
	"context"
	"strings"
	"testing"
)

// The model binding is seeded from AppTown's flags once, unmanaged: the
// row is the provider definition, so an edit survives the next boot's seeding
// (a managed seed once wiped it on every restart).
func TestSeedModelBindingIsRecordedOnceAndStaysEditable(t *testing.T) {
	t.Parallel()
	s := NewService(testDB(t), Dependencies{})
	s.SeedModelBinding(t.Context(), SeedConfig{AgentModel: "anthropic/claude", AgentConcurrency: 2})
	seeded, err := s.Bindings.ByName(t.Context(), "anthropic")
	if err != nil {
		t.Fatal(err)
	}
	if seeded.Capability != CapModel || seeded.Managed || !strings.Contains(seeded.ConfigJSON, "anthropic/claude") {
		t.Fatalf("the model binding is seeded unmanaged from the flag: %+v", seeded)
	}
	if _, err := s.Bindings.Save(context.Background(), "anthropic", CapModel, "pi", `{"model":"anthropic/edited"}`, "fixture"); err != nil {
		t.Fatal(err)
	}
	s.SeedModelBinding(t.Context(), SeedConfig{AgentModel: "anthropic/claude", AgentConcurrency: 2})
	kept, err := s.Bindings.ByName(t.Context(), "anthropic")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(kept.ConfigJSON, "anthropic/edited") || kept.Managed {
		t.Fatalf("a boot's sync overwrote the edited model binding: %+v", kept)
	}
}
