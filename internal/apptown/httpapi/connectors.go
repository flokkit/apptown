package httpapi

import (
	"context"
	"fmt"
	"github.com/flokkit/apptown/internal/access"
	"net/http"
	"slices"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/stock/docs"
)

type Connectors struct {
	integrations *integration.Service
	access       *identity.Service
	deployment   *deployment.Service
	health       ConnectorHealth
	db           *database.DB
	formats      []string
}

func NewConnectors(integrations *integration.Service, access *identity.Service, deployment *deployment.Service, health ConnectorHealth, db *database.DB, formats []string) Connectors {
	return Connectors{integrations: integrations, access: access, deployment: deployment, health: health, db: db, formats: slices.Clone(formats)}
}

// The connector routes' responses.
type (
	// ConnectorEntry is one connector spec and whether it has written docs.
	ConnectorEntry struct {
		integration.ConnectorSpec
		HasDocs bool `json:"hasDocs"`
	}
	ConnectorCatalog struct {
		Catalog []ConnectorEntry `json:"catalog"`
	}
	// IntegrationSaved answers a saved binding with its first health reading.
	IntegrationSaved struct {
		OK     bool               `json:"ok"`
		Health integration.Health `json:"health"`
	}
)

// ConnectorCatalog: GET /api/v1/integrations/catalog — compiled specs
// plus one derived spec per runtime-defined service kind (so admins create
// connectors for new kinds through the same generic form).
func (h Connectors) ConnectorCatalog(w http.ResponseWriter, r *http.Request) {
	out := make([]ConnectorEntry, 0, len(integration.ConnectorCatalog()))
	defaults := h.integrations.CatalogDefaults(r.Context())
	for _, c := range integration.ConnectorCatalog() {
		_, err := docs.FS.ReadFile("integrations/" + c.Capability + "-" + c.Provider + ".admin.md")
		out = append(out, ConnectorEntry{integration.InstancePresets(c, defaults), err == nil})
	}
	for _, k := range h.integrations.Definitions(r.Context()) {
		if len(k.Connector.Fields) > 0 {
			out = append(out, ConnectorEntry{integration.ServiceConnectorSpec(k), false})
		}
	}
	WriteJSON(w, 200, ConnectorCatalog{Catalog: out})
}

// TestIntegration: POST /api/v1/integrations/test {capability, provider, config}
func (h Connectors) TestIntegration(w http.ResponseWriter, r *http.Request) {
	jsonCommand(w, r, jsonBody{errorText: "capability required"}, func(in struct {
		Capability string         `json:"capability"`
		Provider   string         `json:"provider"`
		Config     map[string]any `json:"config"`
	}) {
		if in.Capability == "" {
			WriteError(w, 400, "capability required")
			return
		}

		WriteJSON(w, 200, h.health.Test(r.Context(), in.Capability, in.Provider, in.Config))
	})
}

// SaveIntegration: POST /api/v1/integrations — create/update a USER
// binding (managed bindings are AppTown's; clone under a new name).
func (h Connectors) SaveIntegration(w http.ResponseWriter, r *http.Request) {
	if _, ok := AuthorizeRequest(h.access, w, r, access.CapIntegrationConnector, access.Resource{Type: "instance"}, access.Context{}); !ok {
		return
	}
	jsonCommand(w, r, jsonBody{errorText: "name (slug) required"}, func(in struct {
		Name       string         `json:"name"`
		Capability string         `json:"capability"`
		Provider   string         `json:"provider"`
		Config     map[string]any `json:"config"`
	}) {
		if !project.ValidSlug(in.Name) {
			WriteError(w, 400, "name (slug) required")
			return
		}

		spec := integration.FindConnector(in.Capability, in.Provider)
		if spec == nil && in.Provider == "service" {
			// Runtime-defined service kind: derive the spec live.
			if k := h.integrations.Definition(r.Context(), in.Capability); k != nil && len(k.Connector.Fields) > 0 {
				derived := integration.ServiceConnectorSpec(k)
				spec = &derived
			}
		}
		if spec == nil || spec.Managed || !spec.Multi {
			WriteError(w, 400, "no user-creatable connector for %s/%s", in.Capability, in.Provider)
			return
		}
		if b, err := h.integrations.Bindings.ByName(r.Context(), in.Name); err == nil && b.Managed {
			WriteError(w, 409, "%q is managed by AppTown — clone it under a new name", in.Name)
			return
		}
		// normalize command-ish fields
		if in.Config == nil {
			in.Config = map[string]any{}
		}
		for _, f := range spec.Fields {
			if f.Type == "command" || f.Type == "secretRef" || f.Type == "modelList" {
				in.Config[f.Key] = integration.ConfigStrings(in.Config[f.Key])
			}
		}
		if in.Capability == "registry" {
			format, _ := in.Config["format"].(string)
			if !slices.Contains(h.formats, format) {
				WriteError(w, 400, "unknown registry format %q", format)
				return
			}
			// One binding per format: two npm proxies is a configuration error,
			// not a choice (design/subsystems/registries.md §2).
			if other, ok := integration.RegistryBindingsIn(r.Context(), h.db)[format]; ok && other.Name != in.Name {
				WriteError(w, 409, "format %q is already bound by %q — edit or delete that binding", format, other.Name)
				return
			}
		}
		if _, err := h.integrations.Bindings.Save(r.Context(), in.Name, in.Capability, in.Provider, encodedJSON(in.Config), Actor(r)); err != nil {
			WriteFault(w, fmt.Errorf("saving the binding: %w", err))
			return
		}
		h.deployment.SeedCloudTargets(r.Context(), Actor(r)) // a new cloud account offers its target; a changed one refreshes it
		WriteJSON(w, 200, IntegrationSaved{OK: true, Health: h.health.Test(r.Context(), in.Capability, in.Provider, in.Config)})
	})
}

// DeleteIntegration: DELETE /api/v1/integrations/{name} — user bindings only.
func (h Connectors) DeleteIntegration(w http.ResponseWriter, r *http.Request) {
	if _, ok := AuthorizeRequest(h.access, w, r, access.CapIntegrationConnector, access.Resource{Type: "instance"}, access.Context{}); !ok {
		return
	}
	name := r.PathValue("name")
	if err := h.integrations.Bindings.Delete(r.Context(), name, Actor(r)); err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, Done{OK: true})
}

type ConnectorHealth interface {
	Test(ctx context.Context, capability, provider string, config map[string]any) integration.Health
	Health(ctx context.Context, capability, provider string, secretEnv []string) integration.Health
	// Testable: Test connection asks the kind's service with its configuration.
	Testable(capability string) bool
}
