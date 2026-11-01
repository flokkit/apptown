package httpapi

import (
	"net/http"

	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/ids"
)

type StandingApprovals struct{ owner workflow.StandingRules }

func NewStandingApprovals(owner workflow.StandingRules) StandingApprovals {
	return StandingApprovals{owner: owner}
}
func (h StandingApprovals) List(w http.ResponseWriter, r *http.Request) {
	var id ids.ID
	if value := r.URL.Query().Get("projectId"); value != "" {
		var err error
		id, err = ids.Parse(value)
		if err != nil {
			WriteError(w, 400, "bad project identity")
			return
		}
	}
	out, err := h.owner.List(r.Context(), id)
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, out)
}
func (h StandingApprovals) Preview(w http.ResponseWriter, r *http.Request) {
	jsonCommand(w, r, jsonBody{}, func(in workflow.StandingDefinition) {
		out, err := h.owner.Preview(r.Context(), in)
		if err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 200, out)
	})
}
func (h StandingApprovals) Enable(w http.ResponseWriter, r *http.Request) {
	jsonCommand(w, r, jsonBody{}, func(in workflow.StandingCommand) {
		out, err := h.owner.Enable(r.Context(), in)
		if err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 200, out)
	})
}
func (h StandingApprovals) History(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		WriteError(w, 400, "bad rule identity")
		return
	}
	out, err := h.owner.History(r.Context(), id)
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, out)
}

type StandingDisable struct {
	ExpectedRevision int64 `json:"expectedRevision"`
}

func (h StandingApprovals) Disable(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		WriteError(w, 400, "bad rule identity")
		return
	}
	jsonCommand(w, r, jsonBody{}, func(in StandingDisable) {
		out, err := h.owner.Disable(r.Context(), id, in.ExpectedRevision)
		if err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 200, out)
	})
}

// StandingAssess names work, never caller-supplied evidence or authority.
type StandingAssess struct {
	RunID ids.ID `json:"runId"`
}

func (h StandingApprovals) Assess(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		WriteError(w, 400, "bad rule identity")
		return
	}
	jsonCommand(w, r, jsonBody{}, func(in StandingAssess) {
		if in.RunID.IsZero() {
			WriteError(w, 400, "select an exact run identity")
			return
		}
		out, err := h.owner.Assess(r.Context(), id, in.RunID)
		if err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 200, out)
	})
}
