package httpapi

import (
	"net/http"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/ids"
)

type displayLabelRequest struct {
	ExpectedLabel *string `json:"expectedLabel"`
	Label         string  `json:"label"`
}

func (h Connectors) EditConnectionLabel(w http.ResponseWriter, r *http.Request) {
	if _, ok := AuthorizeRequest(h.access, w, r, access.CapIntegrationConnector, access.Resource{Type: "instance"}, access.Context{Consequential: true}); !ok {
		return
	}
	id, err := pathID(r, "id")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "invalid connection identity")
		return
	}
	jsonCommand(w, r, jsonBody{strict: true}, func(in displayLabelRequest) {
		if in.ExpectedLabel == nil {
			WriteError(w, http.StatusBadRequest, "expectedLabel is required")
			return
		}
		if _, err := h.integrations.Bindings.EditDisplayLabel(r.Context(), id, *in.ExpectedLabel, in.Label, actor(r)); err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, Done{OK: true})
	})
}

func (h Targets) EditTargetLabel(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "invalid target identity")
		return
	}
	jsonCommand(w, r, jsonBody{strict: true}, func(in displayLabelRequest) {
		if in.ExpectedLabel == nil {
			WriteError(w, http.StatusBadRequest, "expectedLabel is required")
			return
		}
		if _, err := h.deployment.EditTargetLabel(r.Context(), id, *in.ExpectedLabel, in.Label); err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, Done{OK: true})
	})
}

func (h Deployments) EditDeploymentLabel(w http.ResponseWriter, r *http.Request) {
	project, ok := projectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	id, err := pathID(r, "id")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "invalid deployment identity")
		return
	}
	jsonCommand(w, r, jsonBody{strict: true}, func(in displayLabelRequest) {
		if in.ExpectedLabel == nil {
			WriteError(w, http.StatusBadRequest, "expectedLabel is required")
			return
		}
		if _, err := h.deployment.EditDeploymentLabel(r.Context(), project.ID, id, *in.ExpectedLabel, in.Label); err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, Done{OK: true})
	})
}

func (h Deployments) ReleaseDefault(w http.ResponseWriter, r *http.Request) {
	project, ok := projectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	value, err := h.deployment.ReadReleaseDefault(r.Context(), project.ID)
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, value)
}

func (h Deployments) SetReleaseDefault(w http.ResponseWriter, r *http.Request) {
	project, ok := projectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	jsonCommand(w, r, jsonBody{strict: true}, func(in struct {
		DeploymentID     ids.ID `json:"deploymentId"`
		ExpectedRevision *int64 `json:"expectedRevision"`
	}) {
		if in.ExpectedRevision == nil {
			WriteError(w, http.StatusBadRequest, "expectedRevision is required")
			return
		}
		value, err := h.deployment.SetReleaseDefault(r.Context(), project.ID, in.DeploymentID, *in.ExpectedRevision)
		if err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, value)
	})
}
