package httpapi

import (
	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/ids"
	"net/http"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/workflow"
)

type ProjectScans struct {
	standing workflow.StandingRules
	releases *deployment.Service
	projects *project.Projects
	work     *workflow.Service
}

func NewProjectScans(projects *project.Projects, work *workflow.Service, standing workflow.StandingRules, releases *deployment.Service) ProjectScans {
	return ProjectScans{projects: projects, work: work, standing: standing, releases: releases}
}

type ProjectScanView struct {
	Standing projectStandingState        `json:"standing"`
	HeldBack []deployment.JanitorHold    `json:"heldBack"`
	Queues   []workflow.QueueStatus      `json:"queues"`
	Scans    workflow.MainScans          `json:"scans"`
	Recent   []workflow.AutomatedAttempt `json:"recent"`
}

func (h ProjectScans) List(w http.ResponseWriter, r *http.Request) {
	p, ok := projectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	scans, err := h.work.LatestMainScans(r.Context(), p.ID)
	if err != nil {
		WriteFault(w, err)
		return
	}
	recent, err := h.work.Attempts(r.Context(), p.ID, "", 20)
	if err != nil {
		WriteFault(w, err)
		return
	}
	queues := []workflow.QueueStatus{}
	for _, queue := range []string{"security", "updates"} {
		state, err := h.work.QueueState(r.Context(), p.ID, queue)
		if err != nil {
			WriteFault(w, err)
			return
		}
		queues = append(queues, state)
	}
	standing, err := h.standing.ProjectState(r.Context(), p.ID)
	if err != nil {
		WriteFault(w, err)
		return
	}
	held, err := h.releases.HeldBack(r.Context(), p.ID)
	if err != nil {
		WriteFault(w, err)
		return
	}
	view := ProjectScanView{Standing: projectStandingState{Enabled: standing.Enabled, Level: standing.Level}, HeldBack: held, Scans: scans, Recent: recent, Queues: queues}
	if standing.Choice != nil {
		view.Standing.Choice = &projectStandingChoice{Person: standing.Choice.Person, At: standing.Choice.At, Reason: standing.Choice.Reason}
	}
	WriteJSON(w, 200, view)
}

func (h ProjectScans) Attempts(w http.ResponseWriter, r *http.Request) {
	p, ok := projectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	recent, err := h.work.Attempts(r.Context(), p.ID, r.URL.Query().Get("queue"), 20)
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, recent)
}

type QueueSkip struct {
	ChangeID ids.ID `json:"changeId"`
}

func (h ProjectScans) Skip(w http.ResponseWriter, r *http.Request) {
	p, ok := projectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	jsonCommand[QueueSkip](w, r, jsonBody{strict: true}, func(body QueueSkip) {
		if err := h.work.SkipUpdate(r.Context(), p.ID, r.PathValue("queue"), body.ChangeID); err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 200, map[string]bool{"ok": true})
	})
}

func (h ProjectScans) SetChoice(w http.ResponseWriter, r *http.Request) {
	p, ok := projectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	jsonCommand(w, r, jsonBody{strict: true}, func(in struct {
		Level  string `json:"level"`
		Reason string `json:"reason"`
	}) {
		if err := h.standing.SetProjectChoice(r.Context(), p.ID, in.Level, in.Reason); err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 200, map[string]bool{"ok": true})
	})
}

type projectStandingState struct {
	Enabled bool                   `json:"enabled"`
	Level   string                 `json:"level"`
	Choice  *projectStandingChoice `json:"choice,omitempty"`
}
type projectStandingChoice struct {
	Person access.Actor `json:"person"`
	At     string       `json:"at"`
	Reason string       `json:"reason"`
}
