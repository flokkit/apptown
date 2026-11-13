package httpapi

import (
	"net/http"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/changeactions"
	"github.com/flokkit/apptown/internal/apptown/project"
)

// ProjectSchedules answers what a project's schedules will do and last did.
type ProjectSchedules struct {
	projects  *project.Projects
	schedules changeactions.Schedules
	now       func() time.Time
}

func NewProjectSchedules(projects *project.Projects, schedules changeactions.Schedules, now func() time.Time) ProjectSchedules {
	return ProjectSchedules{projects: projects, schedules: schedules, now: now}
}

// List: GET /api/v1/projects/{slug}/schedules.
func (h ProjectSchedules) List(w http.ResponseWriter, r *http.Request) {
	p, ok := projectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	got, err := h.schedules.Of(r.Context(), *p, h.now())
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, got)
}

// ScheduleRunView is the change a schedule fired at a person's request opened.
type ScheduleRunView struct {
	ChangeID     ids.ID `json:"changeId"`
	ChangeNumber int64  `json:"changeNumber"`
}

// Run: POST /api/v1/projects/{slug}/schedules/{workflow}/run fires the
// schedule now. The owner checks the caller may open work in the project
// and refuses a busy, stopped or ineligible schedule; a failure after the
// change was committed names it.
func (h ProjectSchedules) Run(w http.ResponseWriter, r *http.Request) {
	p, ok := projectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	ch, err := h.schedules.RunNow(r.Context(), *p, r.PathValue("workflow"), h.now())
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, http.StatusCreated, ScheduleRunView{ChangeID: ch.ID, ChangeNumber: ch.Number})
}
