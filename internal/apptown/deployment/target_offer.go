package deployment

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/fault"
)

// GroupDirectory resolves the instance's groups without owning their records.
type GroupDirectory interface {
	GroupKind(context.Context, string) (string, error)
}

// OfferTarget admits an instance administrator's offer before changing its caps.
// Targets.Offer remains the trusted record operation used by internal assembly.
func (s *Service) OfferTarget(ctx context.Context, name, group string, caps OfferCaps) (*Target, error) {
	if s.authority == nil {
		return nil, access.ErrAuthorityUnavailable
	}
	actor, err := s.authority.Require(ctx, access.Permission{Capability: access.CapPlatformTargets, Resource: access.Resource{Type: "instance"}, Context: access.Context{Route: "deployment.offer-target", Consequential: true}})
	if err != nil {
		return nil, err
	}
	if actor.Principal.Name == "" {
		return nil, errors.New("target offer caller required")
	}
	t, err := s.Targets.ByName(ctx, name)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fault.New(fault.NotFound, fmt.Sprintf("no target named %q", name))
		}
		return nil, err
	}
	if s.Targets.OfferedByConstruction(t) {
		return nil, fault.New(fault.Conflict, fmt.Sprintf("%s is offered to everyone by construction", t.Name))
	}
	group = strings.TrimSpace(group)
	if group == "" {
		return nil, fault.New(fault.Unprocessable, "say which group the offer is to, or * for everyone")
	}
	if group != "*" {
		if s.groups == nil {
			return nil, fault.New(fault.Unavailable, "group directory unavailable")
		}
		kind, err := s.groups.GroupKind(ctx, group)
		if err != nil {
			return nil, err
		}
		if kind == "" {
			return nil, fault.New(fault.NotFound, fmt.Sprintf("no group named %q", group))
		}
	}
	if err := s.Targets.Offer(ctx, t, group, caps, actor.Principal.Name); err != nil {
		return nil, err
	}
	return t, nil
}
