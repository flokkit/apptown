package httpapi

import (
	"net/http"
	"strconv"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
)

type AuditLog struct {
	db       *database.DB
	projects *project.Projects
}

func NewAuditLog(db *database.DB, projects *project.Projects) AuditLog {
	return AuditLog{db: db, projects: projects}
}
func (h AuditLog) AuditLog(w http.ResponseWriter, r *http.Request) {
	var f audit.Filter
	if v := r.URL.Query().Get("project"); v != "" {
		p, err := h.projects.BySlug(r.Context(), v)
		if err != nil {
			writeReadFault(w, err, "no such project %q", v)
			return
		}
		f.ProjectID = p.ID
	}
	f.KindPrefix = r.URL.Query().Get("kind")
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			f.Limit = n
		}
	}
	out, err := audit.Recent(r.Context(), h.db, f)
	if err != nil {
		WriteFault(w, err)
		return
	}
	// Each line names its actor as the person is now: renamed, disabled
	// or removed, by the identity owner's reading.
	actors := make([]access.Actor, len(out))
	for i, e := range out {
		actors[i] = access.Actor{Principal: e.ActorPrincipal, Name: e.Actor, Via: e.ActorVia}
	}
	shown, err := identity.ShowActorsIn(r.Context(), h.db, actors)
	if err != nil {
		WriteFault(w, err)
		return
	}
	for i := range out {
		out[i].ActorShown = shown[i]
	}
	WriteJSON(w, 200, out)
}
