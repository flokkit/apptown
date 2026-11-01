package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/deploy"
)

type IntegrationListing struct {
	integrations *integration.Service
	deployment   *deployment.Service
	health       connectorHealth
}

func NewIntegrationListing(integrations *integration.Service, deployment *deployment.Service, health connectorHealth) IntegrationListing {
	return IntegrationListing{integrations: integrations, deployment: deployment, health: health}
}

// ListIntegrations: GET /api/v1/integrations — bindings, what uses each,
// and retained health.
func (h IntegrationListing) ListIntegrations(w http.ResponseWriter, r *http.Request) {
	bindings, err := h.integrations.Bindings.All(r.Context())
	if err != nil {
		WriteError(w, 500, "%s", err.Error())
		return
	}
	projects, err := h.integrations.Attachments.ProjectsByBinding(r.Context())
	if err != nil {
		WriteError(w, 500, "%s", err.Error())
		return
	}
	deployments, err := h.deployment.StandingByProvider(r.Context())
	if err != nil {
		WriteError(w, 500, "%s", err.Error())
		return
	}
	var list []IntegrationEntry
	for _, b := range bindings {
		e := IntegrationEntry{IntegrationView: IntegrationView{ID: b.ID, Name: b.Name, DisplayLabel: b.DisplayLabel, Capability: b.Capability, Provider: b.Provider, Version: b.Version, UpdatedAt: b.UpdatedAt}, Managed: b.Managed,
			UsedBy: IntegrationUse{Projects: projects[b.Name], Deployments: deployments[b.Name]}}
		if e.UsedBy.Projects == nil {
			e.UsedBy.Projects = []integration.AttachedProject{}
		}
		if e.UsedBy.Deployments == nil {
			e.UsedBy.Deployments = []deployment.ProviderDeployment{}
		}
		_ = json.Unmarshal([]byte(b.ConfigJSON), &e.Config)
		// Inline credentials (host-import residue) never leave the engine:
		// redact before the config reaches the API/UI. The hash covers the
		// UNredacted config — receipts pin the real thing.
		if e.Capability == "model" {
			integration.RedactModelCredentials(e.Config)
		}
		e.ConfigHash = deploy.ConfigHash(b.ConfigJSON)
		list = append(list, e)
	}
	for i := range list {
		list[i].Testable = h.health.Testable(list[i].Capability)
		list[i].Health, err = h.integrations.RecordedHealth(r.Context(), list[i].Name)
		if err != nil {
			WriteFault(w, err)
			return
		}
	}
	WriteJSON(w, 200, IntegrationList{Integrations: list})
}

// IntegrationEntry is one binding with what uses it and its retained health;
// its inline credentials are redacted.
type IntegrationEntry struct {
	IntegrationView
	Managed    bool               `json:"managed"`
	ConfigHash string             `json:"configHash"`
	UsedBy     IntegrationUse     `json:"usedBy"`
	Health     integration.Health `json:"health"`
	// Testable: Test connection asks the service with this configuration,
	// whether or not AppTown checks the kind on every reading.
	Testable bool `json:"testable"`
}

// IntegrationUse is what names an integration directly: the projects that
// attach it and the standing deployments on targets it reaches. A
// AppTown-wide service (source control, the executor, models) is used without
// being named, so an empty use is not "unused".
type IntegrationUse struct {
	Projects    []integration.AttachedProject   `json:"projects"`
	Deployments []deployment.ProviderDeployment `json:"deployments"`
}

type IntegrationList struct {
	Integrations []IntegrationEntry `json:"integrations"`
}

type IntegrationView struct {
	DisplayLabel string         `json:"displayLabel"`
	ID           ids.ID         `json:"id"`
	Name         string         `json:"name"`
	Capability   string         `json:"capability"`
	Provider     string         `json:"provider"`
	Config       map[string]any `json:"config"`
	Version      int            `json:"version"`
	UpdatedAt    string         `json:"updatedAt"`
}
