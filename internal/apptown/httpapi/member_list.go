package httpapi

import (
	"net/http"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity"
)

// ListMembers lists the project's explicit grants.
func (h Members) ListMembers(w http.ResponseWriter, r *http.Request) {
	p, ok := projectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	roles := []GrantView{}
	open := false
	grants, err := h.access.GrantsAt(r.Context(), access.ScopeProject, p.ID.String())
	if err != nil {
		WriteFault(w, err)
		return
	}
	for _, g := range grants {
		if g.SubjectType == "group" && g.Subject == identity.GroupAllUsers {
			open = true
		}
		roles = append(roles, grantOutput(r.Context(), h.projects, g))
	}
	WriteJSON(w, 200, ProjectMemberList{Roles: roles, Open: open})
}

// ProjectMemberList is a project's explicit grants, and whether every
// signed-in person is a member.
type ProjectMemberList struct {
	Roles []GrantView `json:"roles"`
	Open  bool        `json:"open"`
}
