package access

import (
	"context"
	"slices"

	"flokkit.io/forge/internal/ids"
)

// Caller separates authenticated provenance from receipt attribution. Transports
// and explicit internal orchestration establish it; command inputs cannot.
type Caller struct {
	Principal Principal
	Verified  bool
	Receipt   string
}
type callerKey struct{}

func WithCaller(ctx context.Context, c Caller) context.Context {
	c.Principal.Scopes = slices.Clone(c.Principal.Scopes)
	return context.WithValue(ctx, callerKey{}, c)
}
func CallerFrom(ctx context.Context) (Caller, bool) {
	c, ok := ctx.Value(callerKey{}).(Caller)
	c.Principal.Scopes = slices.Clone(c.Principal.Scopes)
	return c, ok
}

// ProjectResources supplies only the facts common authorization needs.
type ProjectResources interface {
	AuthorityResource(context.Context, ids.ID) (Resource, error)
}
type Authorizer interface {
	Require(context.Context, Permission) (Caller, error)
}

// Checked completes a domain's resource and asks the existing authority. It has
// no database, permission cache, or dependency on an application or domain.
type Checked struct {
	Authority Authority
	Projects  ProjectResources
}

func (c Checked) Require(ctx context.Context, p Permission) (Caller, error) {
	if err := ctx.Err(); err != nil {
		return Caller{}, err
	}
	caller, ok := CallerFrom(ctx)
	if !ok || !caller.Verified || caller.Principal.Name == "" {
		return Caller{}, ErrPrincipalRequired
	}
	if c.Authority == nil {
		return Caller{}, ErrAuthorityUnavailable
	}
	if !p.Resource.ProjectID.IsZero() {
		if c.Projects == nil {
			return Caller{}, ErrAuthorityUnavailable
		}
		res, err := c.Projects.AuthorityResource(ctx, p.Resource.ProjectID)
		if err != nil {
			return Caller{}, err
		}
		p.Resource.Group, p.Resource.ProjectName = res.Group, res.ProjectName
	}
	pr, err := c.Authority.Require(ctx, caller.Principal, p)
	if err != nil {
		return Caller{}, err
	}
	caller.Principal = pr
	if caller.Receipt == "" {
		caller.Receipt = pr.Name
	}
	return caller, nil
}
