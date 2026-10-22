package apptown

import (
	"net/http"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/httpapi"
)

func (a *App) requireProjectMember(w http.ResponseWriter, r *http.Request, projectID ids.ID, slug string) bool {
	return httpapi.RequireProjectMember(a.Access, a.authModeName(), w, r.WithContext(callerContext(r)), projectID, slug)
}
