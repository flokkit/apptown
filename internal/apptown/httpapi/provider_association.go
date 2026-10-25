package httpapi

import (
	"net/http"
	"strconv"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/ids"
)

func (h Targets) ProviderAssociation(w http.ResponseWriter, r *http.Request) {
	var in deployment.ProviderAssociationSelection
	if raw := r.URL.Query().Get("connectionId"); raw != "" {
		id, err := ids.Parse(raw)
		if err != nil {
			WriteError(w, 400, "invalid connectionId")
			return
		}
		in.ConnectionID = id
	}
	if raw := r.URL.Query().Get("endpointId"); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			WriteError(w, 400, "invalid endpointId")
			return
		}
		in.EndpointID = id
	}
	out, err := h.deployment.InspectProviderAssociation(r.Context(), r.PathValue("name"), in)
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, out)
}

func (h Targets) AssociateProvider(w http.ResponseWriter, r *http.Request) {
	jsonCommand(w, r, jsonBody{}, func(in deployment.ProviderAssociationSelection) {
		out, err := h.deployment.AssociateProvider(r.Context(), r.PathValue("name"), in)
		if err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 200, out)
	})
}
