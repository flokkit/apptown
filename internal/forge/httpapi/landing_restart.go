package httpapi

import (
	"fmt"
	"net/http"
	"time"

	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/projectlifecycle"
	"flokkit.io/forge/internal/forge/workflow"
)

type LandingRestart struct {
	projects  *project.Projects
	work      *workflow.Service
	lifecycle *projectlifecycle.Service
	db        *database.DB
	now       func() time.Time // the forge's clock, which stamps its receipts; nil is the real one
}

func NewLandingRestart(projects *project.Projects, work *workflow.Service, lifecycle *projectlifecycle.Service, db *database.DB, now func() time.Time) LandingRestart {
	return LandingRestart{projects: projects, work: work, lifecycle: lifecycle, db: db, now: now}
}

// RestartLanding: POST /runs/{id}/restart-landing — THE next action
// when a parked landing meets amended law: abandon the old-law run (its
// receipts stay) and start a fresh landing under the current lane.
func (h LandingRestart) RestartLanding(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r, "id")
	if err != nil {
		WriteError(w, 400, "bad id")
		return
	}
	run, err := workflow.ReadRunIn(r.Context(), h.db, id)
	if err != nil {
		writeReadFault(w, err, "no such run")
		return
	}
	if run.Status != workflow.RunRunning || run.ChangeID.IsZero() {
		WriteError(w, 422, "only a running landing on a change can be restarted")
		return
	}
	p, err := h.projects.ByID(r.Context(), run.ProjectID)
	if err != nil {
		writeReadFault(w, err, "no such project")
		return
	}
	ch, err := workflow.ReadChangeIn(r.Context(), h.db, run.ChangeID)
	if err != nil {
		writeReadFault(w, err, "no such change")
		return
	}
	// Look before leaping: a lane the current rules refuse (a policy
	// amended mid-flight that the lane cannot satisfy) must not cost the
	// parked landing — it stays parked, and the override on its law step
	// is the recorded exception.
	if _, rerr := h.lifecycle.SelectedWorkflow(r.Context(), &p, run.WorkflowName); rerr != nil {
		WriteError(w, 422, "the landing cannot restart under the current rules: %v — override the parked step to record an exception, or fix the lane", rerr)
		return
	}
	if _, err := h.work.Abandon(r.Context(), workflow.StopWork{RunID: run.ID, Reason: "restarted to land under the current rules"}); err != nil {
		WriteFault(w, err)
		return
	}
	fresh, err := h.work.StartRun(r.Context(), workflow.StartChangeRun{ChangeID: ch.ID, Workflow: run.WorkflowName})
	if err != nil {
		WriteError(w, 422, "old landing abandoned but the new one would not start: %v", err)
		return
	}
	audit.At(clock.Or(h.now)()).BestEffort(r.Context(), h.db, Actor(r), p.ID, "landing.restarted", fmt.Sprintf("run %d -> %d", run.ID, fresh.ID), nil)
	WriteJSON(w, 200, RestartedLanding{Run: fresh})
}

// RestartedLanding is the landing run that replaced a stale one.
type RestartedLanding struct {
	Run *workflow.Run `json:"run"`
}
