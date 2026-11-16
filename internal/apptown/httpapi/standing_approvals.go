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
	out, err := h.owner.Current(r.Context())
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, out)
}
func (h StandingApprovals) Save(w http.ResponseWriter, r *http.Request) {
	jsonCommand(w, r, jsonBody{strict: true}, func(in workflow.JanitorChoicesCommand) {
		out, err := h.owner.Save(r.Context(), in)
		if err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 200, out)
	})
}
func (h StandingApprovals) History(w http.ResponseWriter, r *http.Request) {
	out, err := h.owner.History(r.Context())
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, out)
}

type StandingAssess struct {
	Run ids.ID `json:"run"`
}

func (h StandingApprovals) Assess(w http.ResponseWriter, r *http.Request) {
	jsonCommand(w, r, jsonBody{strict: true}, func(in StandingAssess) {
		if in.Run.IsZero() {
			WriteError(w, 400, "select an exact run identity")
			return
		}
		out, err := h.owner.Assess(r.Context(), in.Run)
		if err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 200, out)
	})
}
