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
func (s *Service) CreationTargets(ctx context.Context, packs []string, audience, data string) ([]deployment.CreationTarget, error) {
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
	choices, err := s.deployments.CreationTargets(ctx, group, contracts, audience, data)
	if err != nil {
		return nil, err
	}
	out := make([]deployment.CreationTarget, 0, len(choices))
	for _, choice := range choices {
		f, err := s.identity.FeatureState(ctx, choice.Area)
		if err != nil {
			return nil, err
		}
		if f.Enabled {
			out = append(out, choice)
		}
	}
	return out, nil
}

// A new project cannot select a disabled beta provider, including an implicit
// default. Existing workflow executions keep their declared deployment steps.
func (s *Service) requireCreationFeature(ctx context.Context, in Draft) error {
	caller, ok := access.CallerFrom(ctx)
	if s.deployments == nil || !ok || !caller.Verified || in.Target == "none" {
		return nil
	}
	group, err := s.identity.OwningGroupFor(ctx, caller.Principal, in.Group)
	if err != nil {
		return err
	}
	choices, err := s.deployments.CreationTargets(ctx, group, nil, in.Audience, in.DataClass)
	if err != nil {
		return err
	}
	for _, choice := range choices {
		if choice.Name == in.Target || (in.Target == "" && choice.Default) {
			return s.identity.RequireFeature(ctx, choice.Area)
		}
	}
	return nil
}
