package httpapi

import (
	"net/http"

	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
)

// ChangeFromPath resolves {id} to the Change, answering 400 for a malformed
// id and 404 when there is no such change. false means the handler must
// stop.
func ChangeFromPath(db database.Queryer, w http.ResponseWriter, r *http.Request) (*workflow.Change, bool) {
	id, err := PathID(r, "id")
	if err != nil {
		WriteError(w, 400, "bad id")
		return nil, false
	}
	ch, err := workflow.ReadChangeIn(r.Context(), db, id)
	if err != nil {
		writeReadFault(w, err, "no such change")
		return nil, false
	}
	return ch, true
}

// ChangeAndProject resolves {id} to the Change and its project, answering
// 400 for a malformed id and 404 when either is missing. false means the
// handler must stop.
func ChangeAndProject(db database.Queryer, w http.ResponseWriter, r *http.Request) (*workflow.Change, *project.Project, bool) {
	id, err := PathID(r, "id")
	if err != nil {
		WriteError(w, 400, "bad id")
		return nil, nil, false
	}
	ch, err := workflow.ReadChangeIn(r.Context(), db, id)
	if err != nil {
		writeReadFault(w, err, "no such change")
		return nil, nil, false
	}
	p, err := project.ByIDIn(r.Context(), db, ch.ProjectID)
	if err != nil {
		writeReadFault(w, err, "no such project")
		return nil, nil, false
	}
	return ch, &p, true
}
