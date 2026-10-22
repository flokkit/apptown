package identity

import (
	"context"
	"log"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/database"
)

// HasProjectRole: "works here" in the roles world — any live project-scoped
// grant to the person, one of their groups, or everyone; or root. A viewer is a member for the roster's
// purposes; what they may DO is the kernel's question per act.
func (s *Service) HasProjectRole(ctx context.Context, pr access.Principal, projectID ids.ID) bool {
	if pr.Kind != "user" {
		return false
	}
	pid := projectID.String()
	grants, err := s.ActiveGrants(ctx, pr)
	if _, err := database.Lookup(err); err != nil {
		log.Printf("read failed: %v", err)
	}
	if _, root := access.RootGrant(grants); root {
		return true
	}
	for _, g := range grants {
		if g.ScopeType == access.ScopeProject && g.ScopeID == pid {
			return true
		}
	}
	return false
}
