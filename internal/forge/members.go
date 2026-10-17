package forge

import (
	"net/http"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/forge/httpapi"
)

func (a *App) requireProjectMember(w http.ResponseWriter, r *http.Request, projectID ids.ID, slug string) bool {
	return httpapi.RequireProjectMember(a.Access, a.authModeName(), w, r.WithContext(callerContext(r)), projectID, slug)
}
