package httpapi

import (
	"net/http"

	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/projectlifecycle"
)

type ProjectSettings struct {
	projects  *project.Projects
	access    *identity.Service
	lifecycle *projectlifecycle.Service
}

func NewProjectSettings(projects *project.Projects, access *identity.Service, lifecycle *projectlifecycle.Service) ProjectSettings {
	return ProjectSettings{projects: projects, access: access, lifecycle: lifecycle}
}

// PatchProject renames a project (display name and/or slug). Internal
// references use project_id, so a slug change only moves URLs; audit history
// keeps old slugs as text, which is correct ledger behavior.
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
