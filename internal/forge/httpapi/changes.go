package httpapi

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/workflow"
)

type Changes struct {
	work     *workflow.Service
	projects *project.Projects
	access   *identity.Service
	db       *database.DB
	now      func() time.Time // the forge's clock, which stamps its receipts; nil is the real one
}

func NewChanges(work *workflow.Service, projects *project.Projects, authority *identity.Service, db *database.DB, now func() time.Time) Changes {
	return Changes{work: work, projects: projects, access: authority, db: db, now: now}
}
func (h Changes) ListChanges(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	items, err := h.work.Changes(r.Context(), p.ID, workflow.ChangeList{})
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, items)
}

// handleStartChangeRun: POST /api/v1/changes/{id}/runs {workflow} — start a
// working (or any) workflow run on the Change's branch.
func (h Changes) StartChangeRun(w http.ResponseWriter, r *http.Request) {
	ch, p, ok := ChangeAndProject(h.db, w, r)
	if !ok {
		return
	}
	jsonCommand(w, r, jsonBody{errorText: "workflow required"}, func(in struct {
		Workflow string
	}) {
		if in.Workflow == "" {
			WriteError(w, 400, "workflow required")
			return
		}

		run, err := h.work.StartRun(r.Context(), workflow.StartChangeRun{ChangeID: ch.ID, Workflow: in.Workflow})
		if err != nil {
			WriteFaultOr(w, err, 422)
			return
		}
		audit.At(clock.Or(h.now)()).BestEffort(r.Context(), h.db, Actor(r), p.ID, "change.run-started", fmt.Sprintf("change %d run %d (%s)", ch.ID, run.ID, in.Workflow), nil)
		WriteJSON(w, 201, ChangeRun{Change: *ch, Run: *run})
	})
}

// handleStartChange: POST /api/v1/changes/{id}/start — promote a proposed
// Change: branch + worktree, in_progress.
func (h Changes) StartChange(w http.ResponseWriter, r *http.Request) {
	ch, _, ok := ChangeAndProject(h.db, w, r)
	if !ok {
		return
	}
	out, err := h.work.Begin(r.Context(), ch.ID)
	if err != nil {
		WriteFaultOr(w, err, 409)
		return
	}
	WriteJSON(w, 200, ChangeEnvelope{Change: *out})
}

// handleAbandonChange: POST /api/v1/changes/{id}/abandon — close the Change
// without landing: abandon in-flight runs, drop the worktree, keep the
// branch for forensics.
func (h Changes) AbandonChange(w http.ResponseWriter, r *http.Request) {
	ch, _, ok := ChangeAndProject(h.db, w, r)
	if !ok {
		return
	}
	if ch.State == workflow.ChangeShipped || ch.State == workflow.ChangeAbandoned {
		WriteError(w, 409, "change #%d already %s", ch.ID, ch.State)
		return
	}
	jsonCommand(w, r, jsonBody{optional: true}, func(in struct{ Reason string }) {
		if _, err := h.work.Abandon(r.Context(), workflow.StopWork{ChangeID: ch.ID, Reason: in.Reason}); err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 200, Done{OK: true})
	})
}

// handlePatchChange: PATCH /api/v1/changes/{id} {expectedEditRevision,
// title?, body?, externalRef?} — the cockpit's "For" block is human-editable.
func (h Changes) PatchChange(w http.ResponseWriter, r *http.Request) {
	ch, p, ok := ChangeAndProject(h.db, w, r)
	if !ok {
		return
	}
	jsonCommand(w, r, jsonBody{}, func(in struct {
		Title            *string `json:"title"`
		Body             *string `json:"body"`
		ExternalRef      *string `json:"externalRef"`
		ExpectedRevision *int64  `json:"expectedEditRevision"`
	}) {
		out, err := h.work.Edit(r.Context(), workflow.ChangeEdit{ProjectID: p.ID, ID: ch.ID, Title: in.Title, Body: in.Body, ExternalRef: in.ExternalRef, ExpectedRevision: in.ExpectedRevision})
		if err != nil {
			WriteFaultOr(w, err, 500)
			return
		}
		WriteJSON(w, 200, out)
	})
}

// handleSendBack: POST /api/v1/changes/{id}/sendback {note} — a person
// sends a lane-built change back to its agent WITH WORDS (Letterbox
// change 157: landing gates failed on a stale test runner and a root
// Dockerfile; the only doors were a chat with no credit or a hand re-run
// under Details). The latest working run's agent continues its own
// session with the note; a landing wedged on failed gates is abandoned
// so the change returns to work and can land again afterwards. The
// change page follows the working run.
func (h Changes) SendBack(w http.ResponseWriter, r *http.Request) {
	ch, ok := ChangeFromPath(h.db, w, r)
	if !ok {
		return
	}
	if _, ok := AuthorizeRequest(h.access, w, r, access.CapChangeSendBack, workflow.ChangeResource(r.Context(), h.projects, ch), access.Context{}); !ok {
		return
	}
	jsonCommand(w, r, jsonBody{errorText: "say what to change — the note is what the agent works from"}, func(in struct {
		Note string `json:"note"`
	}) {
		if strings.TrimSpace(in.Note) == "" {
			WriteError(w, 400, "say what to change — the note is what the agent works from")
			return
		}

		result, err := h.work.SendBack(r.Context(), workflow.SendBack{ChangeID: ch.ID, Note: in.Note})
		if err != nil {
			WriteFaultOr(w, err, 500)
			return
		}
		WriteJSON(w, 202, SendBackAccepted{RunID: result.RunID, StepID: result.StepID})
	})
}

func (h Changes) Board(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	// The board is the map of the Change lifecycle (design/subsystems/dev-loop.md):
	// columns are states, not workflow stages. Closed columns are capped —
	// history lives in the overview, not the board.
	items, err := h.work.Changes(r.Context(), p.ID, workflow.ChangeList{})
	if err != nil {
		WriteFault(w, err)
		return
	}
	byState := map[workflow.ChangeState][]workflow.ChangeSummary{}
	for _, it := range items {
		byState[it.State] = append(byState[it.State], it)
	}
	cols := []BoardColumn{}
	for _, st := range []workflow.ChangeState{workflow.ChangeProposed, workflow.ChangeInProgress, workflow.ChangeLanding, workflow.ChangeShipped, workflow.ChangeAbandoned} {
		items := byState[st]
		c := BoardColumn{State: st, Total: len(items), Items: items}
		if c.Items == nil {
			c.Items = []workflow.ChangeSummary{}
		}
		if st.Closed() && len(c.Items) > 8 {
			c.Items = c.Items[:8]
		}
		cols = append(cols, c)
	}
	WriteJSON(w, 200, ChangeBoard{Columns: cols})
}

// The Change routes' responses.
type (
	// ChangeRun is a Change and the run a command started on it.
	ChangeRun struct {
		Change workflow.Change `json:"change"`
		Run    workflow.Run    `json:"run"`
	}
	// ChangeEnvelope carries one Change.
	ChangeEnvelope struct {
		Change workflow.Change `json:"change"`
	}
	// SendBackAccepted names the run and step that took a send-back note.
	SendBackAccepted struct {
		RunID  ids.ID `json:"runId"`
		StepID ids.ID `json:"stepId"`
	}
	// ChangeBoard is a project's Changes in lifecycle columns.
	ChangeBoard struct {
		Columns []BoardColumn `json:"columns"`
	}
	// BoardColumn is one lifecycle state's Changes; closed columns are capped.
	BoardColumn struct {
		State workflow.ChangeState     `json:"state"`
		Total int                      `json:"total"`
		Items []workflow.ChangeSummary `json:"items"`
	}
)
