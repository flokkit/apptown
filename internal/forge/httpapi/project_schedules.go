package httpapi

import (
	"net/http"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/forge/changeactions"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/workflow"
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

// ScheduleList is a project's schedules. EvalCopy: an eval's throwaway copy,
// which runs no schedule, so none is listed.
type ScheduleList struct {
	EvalCopy  bool           `json:"evalCopy"`
	Schedules []ScheduleView `json:"schedules"`
}

// ScheduleView is one schedule. Source is what declares it: today the
// adopted workflow version that carries it. Cron is read in TimeZone, always
// UTC, AppTown's time. Next is when the scheduler next fires it; Due says
// that time has passed and its next tick fires it. Last is absent when it
// never fired. Problem says why the schedule could not be read; Next and
// Last are then unknown, not absent.
type ScheduleView struct {
	Source       ScheduleSource      `json:"source"`
	Cron         string              `json:"cron"`
	TimeZone     string              `json:"timeZone"`
	SkipIfActive bool                `json:"skipIfActive"`
	Next         string              `json:"next,omitempty"`
	Due          bool                `json:"due"`
	Last         *ScheduleFiringView `json:"last,omitempty"`
	Problem      string              `json:"problem,omitempty"`
}

// ScheduleSource is the workflow, and its adopted version, that declares a
// schedule.
type ScheduleSource struct {
	Workflow string `json:"workflow"`
	Version  int    `json:"version"`
}

// ScheduleFiringView is a schedule's last firing: the change it opened, a
// skip because the previous run was still active, a run that could not
// start, or unknown (fired before outcomes were kept).
type ScheduleFiringView struct {
	At       string                   `json:"at"`
	Outcome  workflow.ScheduleOutcome `json:"outcome"`
	ChangeID ids.ID                   `json:"changeId,omitzero"`
	// ChangeNumber names ChangeID within the project; 0 when none or gone.
	ChangeNumber int64 `json:"changeNumber"`
}

// List: GET /api/v1/projects/{slug}/schedules.
func (h ProjectSchedules) List(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	got, err := h.schedules.Of(r.Context(), *p, h.now())
	if err != nil {
		WriteFault(w, err)
		return
	}
	out := ScheduleList{EvalCopy: got.EvalCopy, Schedules: []ScheduleView{}}
	for _, s := range got.Schedules {
		v := ScheduleView{Source: ScheduleSource{Workflow: s.Workflow, Version: s.Version}, Cron: s.Cron, TimeZone: "UTC",
			SkipIfActive: s.SkipIfActive, Due: s.Due, Problem: s.Problem}
		if !s.Next.IsZero() {
			v.Next = s.Next.UTC().Format(time.RFC3339)
		}
		if s.Last != nil {
			v.Last = &ScheduleFiringView{At: s.Last.At.UTC().Format(time.RFC3339), Outcome: s.Last.Outcome, ChangeID: s.Last.ChangeID, ChangeNumber: s.Last.ChangeNumber}
		}
		out.Schedules = append(out.Schedules, v)
	}
	WriteJSON(w, 200, out)
}
