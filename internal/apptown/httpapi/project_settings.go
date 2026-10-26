package httpapi

import (
	"net/http"

	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/projectlifecycle"
)

type ProjectSettings struct {
	projects  *project.Projects
	access    *identity.Service
	lifecycle *projectlifecycle.Service
}

func NewProjectSettings(projects *project.Projects, access *identity.Service, lifecycle *projectlifecycle.Service) ProjectSettings {
	return ProjectSettings{projects: projects, access: access, lifecycle: lifecycle}
}

// PatchProject edits settings; changing its address retains every old address.
func (h ProjectSettings) PatchProject(w http.ResponseWriter, r *http.Request) {
	if !RequireAdmin(h.access, w, r) {
		return
	}
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	jsonCommand(w, r, jsonBody{}, func(in project.SettingsPatch) {
		out, err := h.lifecycle.UpdateSettings(r.Context(), p.ID, in, Actor(r))
		if err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 200, out)
	})
}
