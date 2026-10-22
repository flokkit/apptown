package httpapi

import (
	"net/http"
	"strings"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/integration"
)

type ServiceKinds struct {
	integrations *integration.Service
	access       *identity.Service
}

func NewServiceKinds(integrations *integration.Service, access *identity.Service) ServiceKinds {
	return ServiceKinds{integrations: integrations, access: access}
}

// ServiceKindEntry is one runtime-defined service kind: its parsed
// definition, or the error parsing its YAML.
type ServiceKindEntry struct {
	integration.Definition
	YAML      string `json:"yaml"`
	UpdatedAt string `json:"updatedAt"`
	Error     string `json:"error,omitempty"`
}

type ServiceKindList struct {
	Services []ServiceKindEntry `json:"services"`
}

// ListServiceKinds: GET /api/v1/services
func (h ServiceKinds) ListServiceKinds(w http.ResponseWriter, r *http.Request) {
	kinds, err := h.integrations.ServiceKinds.All(r.Context())
	if err != nil {
		WriteFault(w, err)
		return
	}
	out := []ServiceKindEntry{}
	var raws []ServiceKindEntry
	for _, k := range kinds {
		raws = append(raws, ServiceKindEntry{YAML: k.YAML, UpdatedAt: k.UpdatedAt})
		raws[len(raws)-1].Capability = k.Capability
	}
	for _, e := range raws {
		if k, err := integration.ParseDefinition(e.YAML); err == nil {
			e.Definition = *k
		} else {
			e.Error = err.Error()
		}
		out = append(out, e)
	}
	WriteJSON(w, 200, ServiceKindList{Services: out})
}

// SaveServiceKind: POST /api/v1/services {yaml} — create/update a
// runtime-defined service kind (admin; versionless v1, the YAML is small and
// integrations pin nothing from it — contributions are re-rendered live).
func (h ServiceKinds) SaveServiceKind(w http.ResponseWriter, r *http.Request) {
	if _, ok := AuthorizeRequest(h.access, w, r, access.CapIntegrationServices, access.Resource{Type: "instance"}, access.Context{}); !ok {
		return
	}
	jsonCommand(w, r, jsonBody{errorText: "yaml required"}, func(in struct {
		YAML string `json:"yaml"`
	}) {
		if strings.TrimSpace(in.YAML) == "" {
			WriteError(w, 400, "yaml required")
			return
		}

		k, err := integration.ParseDefinition(in.YAML)
		if err != nil {
			WriteError(w, 400, "%v", err)
			return
		}
		if err := h.integrations.ServiceKinds.Save(r.Context(), k.Capability, in.YAML, k.Title, Actor(r)); err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 200, CapabilitySaved{OK: true, Capability: k.Capability})
	})
}

// DeleteServiceKind: DELETE /api/v1/services/{capability} — blocked
// while any project holds an integration of the kind.
func (h ServiceKinds) DeleteServiceKind(w http.ResponseWriter, r *http.Request) {
	if _, ok := AuthorizeRequest(h.access, w, r, access.CapIntegrationServices, access.Resource{Type: "instance"}, access.Context{}); !ok {
		return
	}
	capability := r.PathValue("capability")
	if err := h.integrations.ServiceKinds.Delete(r.Context(), capability, Actor(r)); err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, Done{OK: true})
}
