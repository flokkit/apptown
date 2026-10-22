package apptown

import (
	"context"

	"github.com/flokkit/apptown/internal/access"
)

// Account-level administration summaries and handler guards. Authority comes
// only from explicit capability grants, held directly or through a group.

// hasPrivilege is the account menu summary; individual actions authorize
// their exact capability and resource.
func (a *App) hasPrivilege(ctx context.Context, user, role, priv string) bool {
	pr := access.Principal{Kind: "user", Name: user, Role: role}
	if user == "" || user == "anonymous" {
		pr = access.Principal{Kind: "anonymous", Name: "anonymous"}
	}
	if priv == "release" {
		return a.holds(ctx, pr, access.CapReleasePromote, access.Resource{Type: "release-target", ID: releaseTargetName})
	}
	for _, c := range access.AmenderCaps {
		if a.holds(ctx, pr, c, a.resourceInstance()) {
			return true
		}
	}
	return false
}
