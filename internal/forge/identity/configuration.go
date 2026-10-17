package identity

import (
	"context"

	"flokkit.io/forge/internal/access"
)

func (s *Service) requireConfiguration(ctx context.Context, pr access.Principal, operation string) error {
	_, err := s.Require(ctx, pr, access.Permission{Capability: access.CapIdentityAuth, Resource: access.Resource{Type: "instance"},
		Context: access.Context{Consequential: true, Route: operation}})
	return err
}
