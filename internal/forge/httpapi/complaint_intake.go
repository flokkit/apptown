package httpapi

import (
	"net/http"
	"strings"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/console"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/complaint"
	"flokkit.io/forge/internal/forge/conversation"
	"flokkit.io/forge/internal/forge/workflow"
)

type ComplaintIntake struct {
	box      *complaint.Box
	sessions *console.Service
	db       *database.DB
}

func NewComplaintIntake(box *complaint.Box, sessions *console.Service, db *database.DB) ComplaintIntake {
	return ComplaintIntake{box: box, sessions: sessions, db: db}
}

// Complain: POST /api/v1/complaints {text, runId?, step?, about?}
// about is apptown (the default), way-of-working or project. A run's own
// credential decides which project a complaint is filed in: a runId naming
// a run of another project is refused, so a complaint lands where its filer
// works (an eval's copy included), never where it says.
// Reachable through the read-only console listener (its second sanctioned
// exception — see GETOnly): complaints are inert, so the write plane's
// no-mutations rule is not what it protects against.
func (h ComplaintIntake) Complain(w http.ResponseWriter, r *http.Request) {
	jsonCommand(w, r, jsonBody{errorText: "a complaint needs text"}, func(in ComplaintText) {
		if strings.TrimSpace(in.Text) == "" {
			WriteError(w, 400, "a complaint needs text")
			return
		}

		source, workflowName, sessionID := "human", "", ""
		var projectID, changeID ids.ID
		if !in.RunID.IsZero() {
			run, err := workflow.ReadRunIn(r.Context(), h.db, in.RunID)
			if err != nil {
				WriteError(w, 500, "reading complaint run: %v", err)
				return
			}
			owner, err := workflow.ReadSourceIn(r.Context(), h.db, workflow.RunRef{ID: run.ID, ProjectID: run.ProjectID})
			if err != nil {
				WriteFault(w, err)
				return
			}
			if caller, ok := access.CallerFrom(r.Context()); ok && caller.Principal.Instrument == "run" && caller.Principal.ProjectID != owner.ProjectID {
				WriteError(w, 403, "run %d is not in the project this run works in", in.RunID)
				return
			}
			source, projectID, workflowName, changeID = "run", owner.ProjectID, owner.WorkflowName, owner.ChangeID
		} else if caller, ok := access.CallerFrom(r.Context()); ok && caller.Principal.Instrument == "run" {
			source, projectID = "run", caller.Principal.ProjectID
		}
		// A console session token identifies the complaining helper exactly.
		if tok := r.Header.Get("X-AppTown-Session-Token"); tok != "" {
			if id, ok := h.sessions.Identity(tok); ok {
				scope := conversation.FromLabels(id.Labels)
				source, projectID, sessionID, changeID = "console", scope.ProjectID, id.SessionID, scope.ChangeID
			}
		}
		id, err := h.box.File(r.Context(), complaint.Entry{ProjectID: projectID, RunID: in.RunID, ChangeID: changeID,
			Source: source, Workflow: workflowName, Step: in.Step, SessionID: sessionID, Actor: Actor(r),
			Text: in.Text, Why: in.Why, Wish: in.Wish, About: in.About})
		if err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 201, ComplaintFiled{OK: true, ID: id})
	})
}

// ComplaintFiled is the id of a complaint just filed.
type ComplaintFiled struct {
	OK bool   `json:"ok"`
	ID ids.ID `json:"id"`
}

// ComplaintText is POST /complaints: what slowed someone down.
type ComplaintText struct {
	Text  string `json:"text"`
	Why   string `json:"why"`
	Wish  string `json:"wish"`
	RunID ids.ID `json:"runId"`
	Step  string `json:"step"`
	About string `json:"about"`
}
