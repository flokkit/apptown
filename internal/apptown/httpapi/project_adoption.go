package httpapi

import (
	"net/http"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/database"
)

type ProjectAdoption struct {
	projects *project.Projects
	adoption *adoption.Service
	ways     *wow.Service
	db       *database.DB
}

func NewProjectAdoption(projects *project.Projects, adoption *adoption.Service, ways *wow.Service, db *database.DB) ProjectAdoption {
	return ProjectAdoption{projects: projects, adoption: adoption, ways: ways, db: db}
}

// ProjectAdoption: GET /projects/{slug}/adoption — the current
// revision and its receipt, plus the history.
func (h ProjectAdoption) ProjectAdoption(w http.ResponseWriter, r *http.Request) {
	p, ok := projectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	// A project with no revision yet has no current receipt: that is
	// absence, not a failure.
	current, err := adoption.CurrentReceiptIn(r.Context(), h.db, p.ID)
	if _, err := database.Lookup(err); err != nil {
		WriteFault(w, err)
		return
	}
	history, err := h.adoption.History(r.Context(), p.ID, 50)
	if err != nil {
		WriteFault(w, err)
		return
	}
	view, err := h.placementOf(r.Context(), p, current)
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, ProjectAdoptionRecord{Current: current, History: history, PlacementView: view})
}

// ProjectAdoptionRecord is a project's current adoption revision, its
// history, and where the project sits against its packs.
type ProjectAdoptionRecord struct {
	Current *adoption.RecordedRevision  `json:"current"`
	History []adoption.RecordedRevision `json:"history"`
	PlacementView
}
