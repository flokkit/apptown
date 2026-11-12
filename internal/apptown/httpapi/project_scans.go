package httpapi

import (
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
	WriteJSON(w, 200, ProjectScanView{Scans: scans, Recent: recent})
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
