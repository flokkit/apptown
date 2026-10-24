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
// project as it is now (its profile and its own overrides), the variant's
// from the profile under test with no overrides.
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
			pattern, _, _, err = s.slots.Resolve(ctx, ids.Nil, profile, slot)
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

// CopyModels gives the project to the models a copy of from runs: its
// profile, and, when withOverrides, its own slot overrides, each with its
// receipt. It is how an eval's copy runs what its source runs (the
// baseline) or the profile under test with nothing of the source's own
// (the variant, profile set and withOverrides false).
func (s *Slots) CopyModels(ctx context.Context, from, to ids.ID, profile string, withOverrides bool, who string) error {
	p, err := project.ByIDIn(ctx, s.db, to)
	if err != nil {
		return err
	}
	if !ValidProfile(profile) {
		return ErrProfileName
	}
	if err := project.New(s.db, nil).SetModelProfile(ctx, to, profile); err != nil {
		return fmt.Errorf("setting the copy's model profile: %w", err)
	}
	if !withOverrides {
		return nil
	}
	overrides, err := queries(s.db).ProjectOverrides(ctx, from)
	if err != nil {
		return err
	}
	for _, o := range overrides {
		if err := s.Set(ctx, o.Slot, to, p.Slug, "", o.Pattern, who); err != nil {
			return err
		}
	}
	return nil
}
