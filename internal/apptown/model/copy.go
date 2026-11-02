package model

import (
	"context"
	"fmt"
	"slices"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/project"
)

// ArmSlots is what a project would run on each slot: AppTown's stock
// slots and every slot the fallback chain names, resolved as Resolve
// resolves them. An eval records it for each arm when it starts: the
// baseline's from the source
// project with its current profile, the variant's from the profile under test.
func (s Selection) ArmSlots(ctx context.Context, projectID ids.ID, profile string) (map[string]string, error) {
	fallbacks, err := s.slots.Fallbacks(ctx)
	if err != nil {
		return nil, err
	}
	names := slices.Clone(StockSlots)
	for slot, to := range fallbacks {
		for _, n := range []string{slot, to} {
			if !slices.Contains(names, n) {
				names = append(names, n)
			}
		}
	}
	out := map[string]string{}
	for _, slot := range names {
		var pattern string
		if !projectID.IsZero() {
			pattern, _, _, err = s.Resolve(ctx, projectID, slot)
		} else {
			pattern, _, _, err = s.slots.resolve(ctx, profile, slot)
		}
		if err != nil {
			return nil, err
		}
		out[slot] = pattern
	}
	return out, nil
}

// OfferedProfile reports whether profile is one the instance offers: a
// profile its slots or its projects name.
func (s *Slots) OfferedProfile(ctx context.Context, profile string) (bool, error) {
	profiles, err := s.Profiles(ctx)
	return slices.Contains(profiles, profile), err
}

// CopyModels sets the profile an eval copy runs.
func (s *Slots) CopyModels(ctx context.Context, to ids.ID, profile string) error {
	if !ValidProfile(profile) {
		return errProfileName
	}
	if err := project.New(s.db, nil).SetModelProfile(ctx, to, profile); err != nil {
		return fmt.Errorf("setting the copy's model profile: %w", err)
	}
	return nil
}
