package httpapi

import (
	"net/http"
	"time"

	"github.com/flokkit/apptown/internal/apptown/workflow"
)

type ProjectUpkeepView struct {
	Active  []runRowView             `json:"active"`
	Open    []OpenChange             `json:"open"`
	History []workflow.ChangeHistory `json:"history"`
}

func (h ProjectOverview) ProjectUpkeep(w http.ResponseWriter, r *http.Request) {
	p, ok := projectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	query := workflow.HistoryQuery{ProjectID: p.ID, Limit: 50}
	for key, dest := range map[string]*time.Time{"from": &query.From, "until": &query.Until} {
		if raw := r.URL.Query().Get(key); raw != "" {
			value, err := time.Parse(time.RFC3339Nano, raw)
			if err != nil {
				WriteError(w, http.StatusBadRequest, "Invalid upkeep span")
				return
			}
			*dest = value
		}
	}
	if !query.From.IsZero() && !query.Until.IsZero() && query.From.After(query.Until) {
		WriteError(w, http.StatusBadRequest, "Invalid upkeep span")
		return
	}
	if !query.From.IsZero() || !query.Until.IsZero() {
		query.Limit = 0
	}
	data, err := h.work.ProjectUpkeep(r.Context(), query)
	if err != nil {
		WriteFault(w, err)
		return
	}
	out := ProjectUpkeepView{Active: workflowRunRows(data.Active, h.work.Now()), Open: []OpenChange{}, History: data.History}
	for _, item := range data.Open {
		out.Open = append(out.Open, OpenChange{AutomatedOrigin: item.AutomatedOrigin, Untouched: item.Untouched, WaitingOn: item.WaitingOn, ChangeID: item.ID, ChangeNumber: item.Number, Title: item.Title, State: item.State, Editing: item.Editing})
	}
	WriteJSON(w, http.StatusOK, out)
}
