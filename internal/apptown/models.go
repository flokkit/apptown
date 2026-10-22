package apptown

import (
	"context"
	"log"
)

// SeedModelSlots: `default` comes from -agent-model, insert-if-absent —
// the DB row is source of truth thereafter (same rule as model bindings).
// (Fallback rows seed in Open — they're structure, not operator choice.)
func (a *App) SeedModelSlots(ctx context.Context) {
	if err := a.Models.SeedDefault(ctx, a.cfg.agentModel); err != nil {
		log.Printf("seeding the default model slot: %v", err)
	}
}
