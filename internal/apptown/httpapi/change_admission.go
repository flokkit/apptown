package httpapi

import (
	"net/http"

	"github.com/flokkit/apptown/internal/apptown/changeactions"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
)

type ChangeAdmission struct {
	projects *project.Projects
	access   *identity.Service
	work     *workflow.Service
	actions  *changeactions.Service
	db       *database.DB
	authMode string
}

func NewChangeAdmission(projects *project.Projects, access *identity.Service, work *workflow.Service, actions *changeactions.Service, db *database.DB, authMode string) ChangeAdmission {
	return ChangeAdmission{projects: projects, access: access, work: work, actions: actions, db: db, authMode: authMode}
}
func (h ChangeAdmission) CreateChange(w http.ResponseWriter, r *http.Request) {
	p, ok := projectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	if !RequireProjectMember(h.access, w, r, p.ID, p.Slug) {
		return
	}
	jsonCommand(w, r, jsonBody{strict: true}, func(in ChangeRequest) {
		if in.TitleFromChat && in.Workflow != "" {
			WriteError(w, 400, "first-message naming requires a chat Change without a workflow")
			return
		}
		if in.Title == "" {
			WriteError(w, 400, "title is required")
			return
		}
		if in.Type == "" {
			in.Type = "feature"
		}
		var err error
		var item *workflow.Change
		var run *workflow.Run
		if in.Workflow == "" {
			item, err = h.work.Create(r.Context(), workflow.ChangeDraft{ProjectID: p.ID, Type: in.Type, Title: in.Title, Body: in.Body, ExternalRef: in.ExternalRef, TitleFromChat: in.TitleFromChat})
		} else {
			item, run, err = h.work.CreateAndRun(r.Context(), workflow.InvokeChange{Change: workflow.ChangeDraft{ProjectID: p.ID, Type: in.Type, Title: in.Title, Body: in.Body}, Workflow: in.Workflow})
		}
		if err != nil {
			WriteFaultOr(w, err, 422)
			return
		}
		// Optional provenance (console invoke, finding Fix buttons): why this
		// work exists, attached as the run's intent artifact.
		if run != nil {
			h.actions.RecordIntent(r.Context(), run.ID, in.Intent, actor(r))
		}
		WriteJSON(w, 201, CreatedChange{Change: *item, Run: run})
	})
}

// LandChange: POST /api/v1/changes/{id}/land — launch the landing
// workflow (design/subsystems/dev-loop.md): the only path to merge + ship + release.
// Syncs the stock `land` workflow into the project on first use.
func (h ChangeAdmission) LandChange(w http.ResponseWriter, r *http.Request) {
	ch, p, ok := changeAndProject(h.db, w, r)
	if !ok {
		return
	}
	if ch.State != workflow.ChangeInProgress {
		WriteError(w, 409, "change #%d is %s — only in-progress changes land", ch.ID, ch.State)
		return
	}
	run, err := h.actions.RequestLanding(r.Context(), p, ch, actor(r))
	if err != nil {
		// A classified refusal (another landing, authority) gets its kind
		// and code; anything else stays the 422 it always was.
		WriteFaultOr(w, err, 422)
		return
	}
	WriteJSON(w, 201, ChangeRun{Change: *ch, Run: *run})
}

// RecheckLanding: POST /api/v1/changes/{id}/recheck — after fixing what a
// landing gate rejected, run the change's landing's failed automatic gates
// again: what apptown recheck does in a change chat, from anywhere. It never
// touches approvals; Work checks the caller may request the landing.
func (h ChangeAdmission) RecheckLanding(w http.ResponseWriter, r *http.Request) {
	ch, _, ok := changeAndProject(h.db, w, r)
	if !ok {
		return
	}
	rechecked, err := h.work.RecheckLanding(r.Context(), workflow.LandingRecheck{ChangeID: ch.ID})
	if err != nil {
		WriteFaultOr(w, err, 422)
		return
	}
	WriteJSON(w, 200, recheckStarted(rechecked))
}

// CreatedChange is a new Change and the run started on it, if any.
type CreatedChange struct {
	Change workflow.Change `json:"change"`
	Run    *workflow.Run   `json:"run"`
}

// ChangeRequest is POST /projects/{slug}/changes: a change to open and the
// workflow to start on it.
type ChangeRequest struct {
	Type          string `json:"type"`
	Title         string `json:"title"`
	Body          string `json:"body"`
	Workflow      string `json:"workflow"`
	Intent        string `json:"intent"`
	ExternalRef   string `json:"externalRef"`
	TitleFromChat bool   `json:"titleFromChat"`
}
