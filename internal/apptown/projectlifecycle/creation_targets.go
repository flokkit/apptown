package projectlifecycle

import (
	"context"
	"errors"
	"io/fs"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
)

// creationContracts reads declarations, without inferring a runtime from pack names.
// Packs without a starter contract leave compatibility unknown until code exists.
func (s *Service) creationContracts(packs []string) ([]*appcontract.Contract, error) {
	var out []*appcontract.Contract
	for _, name := range packs {
		seed := s.ways.Seed(name)
		if seed == nil {
			continue
		}
		raw, err := fs.ReadFile(seed, ".apptown/app.yaml")
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		c, err := appcontract.Parse(raw)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// CreationTargets resolves offers for the creator's owning group, before a project exists.
func (s *Service) CreationTargets(ctx context.Context, packs []string) ([]deployment.CreationTarget, error) {
	caller, ok := access.CallerFrom(ctx)
	if !ok || !caller.Verified {
		return nil, access.ErrPrincipalRequired
	}
	group, err := s.identity.OwningGroupFor(ctx, caller.Principal, "")
	if err != nil {
		return nil, err
	}
	if _, err := s.identity.Require(ctx, caller.Principal, access.Permission{Capability: access.CapProjectCreate, Resource: access.Resource{Type: "group", ID: group}}); err != nil {
		return nil, err
	}
	if len(packs) > 0 {
		if _, err := s.OfferedPacks(ctx, packs); err != nil {
			return nil, err
		}
	}
	contracts, err := s.creationContracts(packs)
	if err != nil {
		return nil, err
	}
	return s.deployments.CreationTargets(ctx, group, contracts)
}
