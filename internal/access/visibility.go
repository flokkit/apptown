package access

import (
	"context"

	"flokkit.io/forge/internal/ids"
)

// ProjectVisibility evaluates a principal against complete current project facts.
// Returned IDs are observations for read filtering, never permission to mutate.
type ProjectVisibility interface {
	VisibleProjects(context.Context, Principal, Capability) ([]ids.ID, error)
}

// ReadAuthorizer keeps caller establishment and aggregate visibility at the
// same owner boundary as explicit resource authorization.
type ReadAuthorizer interface {
	Authorizer
	VisibleProjects(context.Context, Capability) ([]ids.ID, error)
}

func (c Checked) VisibleProjects(ctx context.Context, capability Capability) ([]ids.ID, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	caller, ok := CallerFrom(ctx)
	if !ok || !caller.Verified || caller.Principal.Name == "" {
		return nil, ErrPrincipalRequired
	}
	visibility, ok := c.Authority.(ProjectVisibility)
	if !ok {
		return nil, ErrAuthorityUnavailable
	}
	return visibility.VisibleProjects(ctx, caller.Principal, capability)
}
