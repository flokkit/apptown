package httpapi

import (
	"github.com/flokkit/apptown/internal/ids"
	"net/http"

	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/workflow"
)

type ProjectScans struct {
	projects *project.Projects
	work     *workflow.Service
}

func NewProjectScans(projects *project.Projects, work *workflow.Service) ProjectScans {
	return ProjectScans{projects, work}
}

type ProjectScanView struct {
	Queues []workflow.QueueStatus      `json:"queues"`
	Scans  workflow.MainScans          `json:"scans"`
	Recent []workflow.AutomatedAttempt `json:"recent"`
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
	WriteJSON(w, 200, ProjectScanView{Scans: scans, Recent: recent, Queues: queues})
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
