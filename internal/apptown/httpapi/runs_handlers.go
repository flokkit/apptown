package httpapi

import (
	"net/http"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/workflow"
)

func (h Runs) Advance(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r, "id")
	if err != nil {
		WriteError(w, 400, "bad id")
		return
	}
	res, err := h.work.Advance(r.Context(), id)
	if err != nil {
		WriteFaultOr(w, err, 422)
		return
	}
	WriteJSON(w, 200, res)
}

func (h Runs) StepRun(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r, "id")
	if err != nil {
		WriteError(w, 400, "bad id")
		return
	}
	jsonCommand(w, r, jsonBody{optional: true}, func(body struct {
		Mode     string `json:"mode"`     // "", "continue", "restart" — agent/pi retries only
		Feedback string `json:"feedback"` // human send-back note; becomes the resumed agent's message
		// AcceptUncopiedData: a deploy step refused because the copy taken
		// before its release was incomplete, retried by a person releasing
		// anyway (recorded with the attempt).
		AcceptUncopiedData bool `json:"acceptUncopiedData"`
	}) {
		mode, err := workflow.ParseRetryMode(body.Mode)
		if err != nil {
			WriteFaultOr(w, err, 500)
			return
		}
		out, err := h.work.RunStep(r.Context(), workflow.RetryCommand{StepID: id, Mode: mode, Feedback: body.Feedback, AcceptUncopiedData: body.AcceptUncopiedData})
		if err != nil {
			WriteFault(w, err)
			return
		}
		if out.Queued {
			WriteJSON(w, 202, struct {
				Status workflow.StepState `json:"status"`
			}{workflow.StatusPending})
		} else {
			WriteJSON(w, 200, out.Step)
		}
	})
}

func (h Runs) Approve(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r, "id")
	if err != nil {
		WriteError(w, 400, "bad id")
		return
	}
	jsonCommand(w, r, jsonBody{}, func(in StepDecision) {
		decision, err := workflow.ParseApprovalDecision(in.Decision)
		if err != nil {
			WriteError(w, 400, "decision must be approve or reject")
			return
		}
		step, err := h.work.Approve(r.Context(), workflow.Approval{StepID: id, Decision: decision, Comment: in.Comment, Shown: in.AlsoApproves, Adds: in.Adds})
		if err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 200, step)
	})
}

func (h Runs) Override(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r, "id")
	if err != nil {
		WriteError(w, 400, "bad id")
		return
	}
	jsonCommand(w, r, jsonBody{}, func(in struct{ User, Reason string }) {
		step, err := h.work.Override(r.Context(), workflow.Override{StepID: id, Reason: in.Reason})
		if err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 200, step)
	})
}

// AbandonRun: POST /api/v1/runs/{id}/abandon {reason} — the run
// lifecycle verb for work that will not continue. Terminal like done/failed
// but honest about why: nothing is deleted, receipts stay, the run simply
// stops counting as in-flight everywhere status='running' gates. Child runs
// (workflow/call) are abandoned with their root.
func (h Runs) AbandonRun(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r, "id")
	if err != nil {
		WriteError(w, 400, "bad id")
		return
	}
	jsonCommand(w, r, jsonBody{optional: true}, func(in struct {
		Reason string `json:"reason"`
	}) {
		result, err := h.work.Abandon(r.Context(), workflow.StopWork{RunID: id, Reason: in.Reason})
		if err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 200, RunsAbandoned{OK: true, Abandoned: result.RunIDs})
	})
}

// RunsAbandoned names the runs a stop abandoned.
type RunsAbandoned struct {
	OK        bool     `json:"ok"`
	Abandoned []ids.ID `json:"abandoned"`
}

// RecheckStage: POST /api/v1/runs/{id}/recheck — run the current stage's
// checks again when its gate is missing evidence it can never get otherwise.
// Every check and approval of the stage runs again; earlier results stay in
// the run's record.
func (h Runs) RecheckStage(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r, "id")
	if err != nil {
		WriteError(w, 400, "bad id")
		return
	}
	out, err := h.work.RecheckStage(r.Context(), id)
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, StageRechecked{RunID: out.RunID, Stage: out.Stage, Missing: out.Missing})
}

// StageRechecked names the stage whose checks run again and what it was missing.
type StageRechecked struct {
	RunID   ids.ID   `json:"runId"`
	Stage   string   `json:"stage"`
	Missing []string `json:"missing"`
}

// StepDecision is POST /steps/{id}/approve: a person's decision on an
// approval step, with what the client showed them.
type StepDecision struct {
	User     string `json:"user"`
	Decision string `json:"decision"`
	Comment  string `json:"comment"`
	// AlsoApproves is what the client showed of the landing's approvals.
	AlsoApproves []string `json:"alsoApproves"`
	// Adds is the digest of what the change adds as the client showed it
	// (the step's question).
	Adds string `json:"adds"`
}
