package identity

import "github.com/flokkit/apptown/internal/access"

// HasRole reports whether the grants carry a role at a scope (a test-side assertion).
func HasRole(grants []access.Grant, role string, scope access.ScopeKind, scopeID string) bool {
	for _, g := range grants {
		if g.RoleName == role && g.ScopeType == scope && g.ScopeID == scopeID {
			return true
		}
	}
	return false
}
