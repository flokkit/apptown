package identity

import (
	"context"
	"strings"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/access"
)

// Scoped is the access.Authority supplied to a subsystem's adapter. Only attribution
// is bound here: every call still observes the account, policy and live grants.
func Scoped(svc *Service, subsystem string) access.Authority {
	return scopedAuthority{service: svc, subsystem: strings.TrimSpace(subsystem)}
}

type scopedAuthority struct {
	service   *Service
	subsystem string
}

func (a scopedAuthority) Require(ctx context.Context, pr access.Principal, p access.Permission) (access.Principal, error) {
	if a.service == nil || a.subsystem == "" {
		return access.Principal{}, access.ErrAuthorityUnavailable
	}
	if p.Context.Route == "" {
		p.Context.Route = a.subsystem
	} else if p.Context.Route != a.subsystem && !strings.HasPrefix(p.Context.Route, a.subsystem+":") {
		p.Context.Route = a.subsystem + ":" + p.Context.Route
	}
	return a.service.Require(ctx, pr, p)
}

func (a scopedAuthority) VisibleProjects(ctx context.Context, pr access.Principal, capability access.Capability) ([]ids.ID, error) {
	if a.service == nil || a.subsystem == "" {
		return nil, access.ErrAuthorityUnavailable
	}
	return a.service.VisibleProjects(ctx, pr, capability)
}
