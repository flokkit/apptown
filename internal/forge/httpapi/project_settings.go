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

// PatchProject edits a project's settings: its display name, icon, line
// and group. Its slug is fixed (project.ErrSlugFixed): it names the
// repository's folder and resources outside AppTown, which a rename would
// not move.
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
