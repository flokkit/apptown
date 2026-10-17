package identity

import (
	"context"
	"fmt"
	"strings"

	"flokkit.io/forge/internal/access"
)

// OwningGroupFor decides where a project is created (§3A "Where a project
// is created"): the group asked for, else the person's default group, else
// their personal group (created on the spot). It never asks; the kernel
// decides afterwards whether the person may create there.
func (s *Service) OwningGroupFor(ctx context.Context, pr access.Principal, asked string) (string, error) {
	if asked = strings.TrimSpace(asked); asked != "" {
		kind, err := s.GroupKind(ctx, asked)
		if err != nil {
			return "", err
		}
		if kind == "" {
			return "", fmt.Errorf("no such group %q", asked)
		}
		return asked, nil
	}
	if pr.Kind != "user" {
		return s.SovereignGroup(ctx)
	}
	user := pr.Name
	def, err := s.DefaultGroup(ctx, user)
	if err != nil {
		return "", err
	}
	if def != "" {
		return def, nil
	}
	// A personal group is minted for people who may create at all (Project
	// creator, the sovereign): owning a personal group carries project:create
	// in it, so minting one for everyone would make everyone a creator. For
	// anyone else the name is what it would be, and the kernel refuses there.
	if s.Evaluate(ctx, pr, access.DelegationOf(pr), access.CapProjectCreate, access.Resource{Type: "instance"}, access.Context{}).Allow {
		return s.PersonalGroup(ctx, user, user)
	}
	return user, nil
}
