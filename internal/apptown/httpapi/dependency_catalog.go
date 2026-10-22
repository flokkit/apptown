package httpapi

import (
	"net/http"
	"strings"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/dependency"
	"github.com/flokkit/apptown/internal/apptown/identity"
)

type Dependencies struct {
	catalog *dependency.Catalog
	access  *identity.Service
}

func NewDependencies(catalog *dependency.Catalog, access *identity.Service) Dependencies {
	return Dependencies{catalog: catalog, access: access}
}

// DependencyList is the dependency catalog.
type DependencyList struct {
	Dependencies []dependency.Kind `json:"dependencies"`
}

// CapabilitySaved answers a saved catalog entry with its capability.
type CapabilitySaved struct {
	OK         bool   `json:"ok"`
	Capability string `json:"capability"`
}

// ListDependencies: GET /api/v1/dependencies — the catalog.
func (h Dependencies) ListDependencies(w http.ResponseWriter, r *http.Request) {
	kinds, err := h.catalog.All(r.Context())
	if err != nil {
		WriteFault(w, err)
		return
	}
	out := DependencyList{Dependencies: make([]dependency.Kind, 0, len(kinds))}
	for _, k := range kinds {
		out.Dependencies = append(out.Dependencies, *k)
	}
	WriteJSON(w, 200, out)
}

// SaveDependency: POST /api/v1/dependencies {yaml} — add or update a
// custom dependency kind (stock kinds are read-only; clone under a new
// capability to customize).
func (h Dependencies) SaveDependency(w http.ResponseWriter, r *http.Request) {
	if _, ok := AuthorizeRequest(h.access, w, r, access.CapIntegrationDeps, access.Resource{Type: "instance"}, access.Context{}); !ok {
		return
	}
	jsonCommand(w, r, jsonBody{errorText: "yaml required"}, func(in struct {
		YAML string `json:"yaml"`
	}) {
		if strings.TrimSpace(in.YAML) == "" {
			WriteError(w, 400, "yaml required")
			return
		}

		d, err := h.catalog.Save(r.Context(), in.YAML, Actor(r))
		if err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 200, CapabilitySaved{OK: true, Capability: d.Capability})
	})
}
