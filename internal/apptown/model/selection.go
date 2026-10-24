package model

import (
	"context"
	"fmt"

	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/ids"
)

// NoModelRefusal is why model work is not started when its model resolves
// to nothing: on a fresh AppTown nobody has added a key yet, and an agent
// would start only to fail with its own advice. Workflow admission (a run
// with a model step) and chat admission both refuse with it.
const NoModelRefusal = "no AI model is set up on this AppTown yet: an administrator adds an Anthropic or OpenRouter API key on the Home page, then start this again"

// Selection combines the project's current profile with slot fallback policy.
// Project and slot changes are read on use.
type Selection struct {
	slots *Slots
}

func NewSelection(slots *Slots) Selection {
	return Selection{slots: slots}
}

func (s Selection) profile(ctx context.Context, projectID ids.ID) (string, error) {
	if projectID.IsZero() {
		return StandardProfile, nil
	}
	p, err := project.ByIDIn(ctx, s.slots.db, projectID)
	if err != nil {
		return "", fmt.Errorf("reading the project's model profile: %w", err)
	}
	return ProfileOf(p.ModelProfile), nil
}

// Resolve selects against the project's current profile and current slot mappings.
// A zero project id selects the standard profile. A failed or missing project
// read is an error, as is any failed slot read ([Slots.Resolve]); the returned
// values otherwise have the meaning and fallback rules of [Slots.Resolve].
func (s Selection) Resolve(ctx context.Context, projectID ids.ID, requested string) (pattern, slot, profile string, err error) {
	proj, err := s.profile(ctx, projectID)
	if err != nil {
		return "", "", "", err
	}
	return s.slots.Resolve(ctx, projectID, proj, requested)
}
