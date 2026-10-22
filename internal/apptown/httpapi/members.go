package httpapi

import (
	"fmt"
	"net/http"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/project"
)

// Members translates project membership requests; Identity owns grant effects.
type Members struct {
	projects *project.Projects
	access   *identity.Service
}

func NewMembers(projects *project.Projects, authority *identity.Service) Members {
	return Members{projects: projects, access: authority}
}
func (h Members) Add(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	if _, ok := AuthorizeRequest(h.access, w, r, access.CapProjectManageMembers, project.ObservedResource(r.Context(), h.projects, p), access.Context{}); !ok {
		return
	}
	jsonCommand(w, r, jsonBody{errorText: fmt.Sprintf("%s", identity.ErrMemberGrantee)}, func(in struct{ Grantee, Role string }) {
		if err := h.access.AddProjectMember(r.Context(), p.ID, in.Grantee, in.Role, Actor(r)); err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 200, Done{OK: true})
	})
}
