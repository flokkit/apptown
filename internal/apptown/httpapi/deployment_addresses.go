package httpapi

import (
	"net/http"

	"github.com/flokkit/apptown/internal/ids"
)

func (h Deployments) addressIDs(w http.ResponseWriter, r *http.Request) (ids.ID, ids.ID, bool) {
	p, ok := projectFromPath(h.projects, w, r)
	if !ok {
		return ids.Nil, ids.Nil, false
	}
	id, err := pathID(r, "id")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "invalid deployment identity")
		return ids.Nil, ids.Nil, false
	}
	return p.ID, id, true
}

func (h Deployments) DeploymentAddress(w http.ResponseWriter, r *http.Request) {
	project, id, ok := h.addressIDs(w, r)
	if !ok {
		return
	}
	value, err := h.deployment.ReadAddress(r.Context(), project, id)
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, value)
}

func (h Deployments) EditDeploymentAddress(w http.ResponseWriter, r *http.Request) {
	project, id, ok := h.addressIDs(w, r)
	if !ok {
		return
	}
	jsonCommand(w, r, jsonBody{strict: true}, func(in struct {
		ExpectedRevision *int64 `json:"expectedRevision"`
		CustomHostname   string `json:"customHostname"`
	}) {
		if in.ExpectedRevision == nil {
			WriteError(w, http.StatusBadRequest, "expectedRevision is required")
			return
		}
		value, err := h.deployment.EditAddress(r.Context(), project, id, *in.ExpectedRevision, in.CustomHostname)
		if err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, value)
	})
}
