package model

import (
	"context"
	"flokkit.io/forge/internal/ids"
	"log"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/project"
)

// Selection combines the project's current profile with slot fallback policy.
// The instance fallback is immutable; project and slot changes are read on use.
type Selection struct {
	slots        *Slots
	defaultModel string
}

func NewSelection(slots *Slots, defaultModel string) Selection {
	return Selection{slots: slots, defaultModel: defaultModel}
}

func (s Selection) profile(ctx context.Context, projectID ids.ID) string {
	if projectID.IsZero() {
		return StandardProfile
	}
	p, err := project.ByIDIn(ctx, s.slots.db, projectID)
	if _, err := database.Lookup(err); err != nil {
		log.Printf("read failed: %v", err)
	}
	return ProfileOf(p.ModelProfile)
}

// Resolve selects against the project's current profile and current slot mappings.
// A missing or unreadable project uses the standard profile; read errors are logged.
// The returned values have the same meaning and fallback rules as [Slots.Resolve].
func (s Selection) Resolve(ctx context.Context, projectID ids.ID, requested string) (pattern, slot, profile string) {
	return s.slots.Resolve(ctx, projectID, s.profile(ctx, projectID), requested, s.defaultModel)
}
