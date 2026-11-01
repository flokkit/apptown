package httpapi

import (
	"encoding/json"
	"net/http"
	"slices"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/docker"
)

type IntegrationListing struct {
	integrations *integration.Service
	deployment   *deployment.Service
	health       connectorHealth
	docker       *docker.Scope
	formats      []string
	now          func() time.Time // AppTown's clock, which dates a health reading
}

func NewIntegrationListing(integrations *integration.Service, deployment *deployment.Service, health connectorHealth, docker *docker.Scope, formats []string, now func() time.Time) IntegrationListing {
	return IntegrationListing{integrations: integrations, deployment: deployment, health: health, docker: docker, formats: slices.Clone(formats), now: clock.Or(now)}
}

// ListIntegrations: GET /api/v1/integrations — bindings, what uses each,
// and live health.
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
	// Health after the rows are materialized (SQLite pool: never query while
	// iterating).
	for i := range list {
		list[i].Testable = h.health.Testable(list[i].Capability)
		if list[i].Capability == integration.CapContainerRegistry {
			list[i].Health = h.health.Test(r.Context(), list[i].Capability, list[i].Provider, list[i].Config)
			continue
		}
		if list[i].Capability == "registry" {
			// cheap shape check; the connector Test button does a real probe
			format, _ := list[i].Config["format"].(string)
			url, _ := list[i].Config["url"].(string)
			switch {
			case !slices.Contains(h.formats, format):
				list[i].Health = integration.Health{OK: false, Detail: "unknown format " + format}
			case url == "":
				list[i].Health = integration.Health{OK: false, Detail: "no url configured"}
			default:
				list[i].Health = integration.Health{OK: true, Detail: format + " → " + url}
			}
			list[i].Health = list[i].Health.At(h.now())
			continue
		}
		if list[i].Capability == "scanner" {
			// cheap local check; the connector Test button does real pulls
			image, _ := list[i].Config["image"].(string)
			switch {
			case image == "":
				list[i].Health = integration.Health{OK: false, Detail: "no image configured"}
			case h.docker.Absent():
				list[i].Health = integration.Health{OK: true, Detail: "image " + image + " pulls on the worker on first use"}
			default:
				_, found, err := h.docker.Image(r.Context(), image)
				switch {
				case err != nil:
					list[i].Health = integration.Health{OK: false, Detail: err.Error()}
				case found:
					list[i].Health = integration.Health{OK: true, Detail: "image " + image + " present"}
				default:
					list[i].Health = integration.Health{OK: true, Detail: "image " + image + " not pulled yet (pulls on first use)"}
				}
			}
			list[i].Health = list[i].Health.At(h.now())
			continue
		}
		var se []string
		if raw, ok := list[i].Config["secretEnv"].([]any); ok {
			for _, v := range raw {
				if sv, ok := v.(string); ok {
					se = append(se, sv)
				}
			}
		}
		list[i].Health = h.health.Health(r.Context(), list[i].Capability, list[i].Provider, se)
	}
	for i := range list {
		list[i].Health = list[i].Health.ForDisplay()
	}
	WriteJSON(w, 200, IntegrationList{Integrations: list})
}

// IntegrationEntry is one binding with what uses it and its live health;
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
