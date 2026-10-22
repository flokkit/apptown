package httpapi

import (
	"net/http"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/projectlifecycle"
)

type ProjectCreation struct {
	lifecycle *projectlifecycle.Service
	access    *identity.Service
}

func NewProjectCreation(lifecycle *projectlifecycle.Service, authority *identity.Service) ProjectCreation {
	return ProjectCreation{lifecycle: lifecycle, access: authority}
}
func (h ProjectCreation) Create(w http.ResponseWriter, r *http.Request) {
	jsonCommand(w, r, jsonBody{}, func(in ProjectDraft) {
		caller, _ := access.CallerFrom(r.Context())
		pr := caller.Principal
		group, err := h.access.OwningGroupFor(r.Context(), pr, in.Group)
		if err != nil {
			WriteError(w, 400, "%v", err)
			return
		}
		// project:create at the owning group (a personal group's owner has it;
		// so does a Project creator at the instance and an instance administrator).
		if _, ok := AuthorizeRequest(h.access, w, r, access.CapProjectCreate, access.Resource{Type: "group", ID: group}, access.Context{}); !ok {
			return
		}
		p, err := h.lifecycle.Create(r.Context(), projectlifecycle.Draft{Slug: in.Slug, Name: in.Name, Namespace: in.Namespace, Blurb: in.Blurb, Icon: in.Icon, Group: group, Actor: Actor(r), Pack: in.Pack})
		if err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 201, p)
	})
}

// ProjectDraft is POST /projects: the project to create.
type ProjectDraft struct {
	Slug      string `json:"slug"`
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Blurb     string `json:"blurb"`
	Icon      string `json:"icon"`
	Group     string `json:"group"`
	// Pack is the pack the caller applies next; one not offered refuses
	// the creation before anything is made.
	Pack string `json:"pack"`
}
