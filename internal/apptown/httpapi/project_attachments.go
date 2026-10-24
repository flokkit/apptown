package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/changeactions"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/stock/docs"
)

type ProjectAttachments struct {
	projects     *project.Projects
	integrations *integration.Service
	access       *identity.Service
	actions      *changeactions.Service
	db           *database.DB
	publicHost   string
	now          func() time.Time // AppTown's clock, which stamps its receipts; nil is the real one
}

func NewProjectAttachments(projects *project.Projects, integrations *integration.Service, access *identity.Service, actions *changeactions.Service, db *database.DB, publicHost string, now func() time.Time) ProjectAttachments {
	return ProjectAttachments{projects: projects, integrations: integrations, access: access, actions: actions, db: db, publicHost: publicHost, now: now}
}

// The project integration routes' responses.
type (
	// AttachmentView is one of a project's integrations; secret-bearing
	// resource values are redacted.
	AttachmentView struct {
		Capability string         `json:"capability"`
		Title      string         `json:"title"`
		Connector  string         `json:"connector"`
		Provenance string         `json:"provenance"`
		Resource   map[string]any `json:"resource"`
		CreatedAt  string         `json:"createdAt"`
		// Whether the APP has to do anything with this attachment. A
		// container registry is used by AppTown's own deploy, not by the
		// app's code — there is nothing to wire, so it must not nag.
		Wirable bool `json:"wirable"`
		// The developer guide for this capability, and (errors) when the
		// provider last saw an event from this app — the proof behind
		// "wired in", per project, from data the card already fetches.
		Docs      string `json:"docs,omitempty"`
		LastEvent string `json:"lastEvent,omitempty"`
		// The change that wires the app into it (0 = not started): the
		// attachment's honest state from the app's side.
		Wire *IntegrationWire `json:"wire,omitempty"`
	}
	AttachmentList struct {
		Attachments []AttachmentView `json:"attachments"`
	}
	// Attachable is a capability the pickers offer, with its connectors.
	Attachable struct {
		Capability     string              `json:"capability"`
		Title          string              `json:"title"`
		Description    string              `json:"description"`
		Provisionable  bool                `json:"provisionable"`
		Connectors     []string            `json:"connectors"`
		ResourceFields []integration.Field `json:"resourceFields"`
	}
	AttachableList struct {
		Attachables []Attachable `json:"attachables"`
	}
	// AttachmentEntered answers a directly entered attachment.
	AttachmentEntered struct {
		OK         bool   `json:"ok"`
		Capability string `json:"capability"`
		Connector  string `json:"connector"`
		Provenance string `json:"provenance"`
	}
	// IntegrationWiring is the change opened to wire the app in, and its run
	// when one started.
	IntegrationWiring struct {
		Change workflow.Change `json:"change"`
		Run    *workflow.Run   `json:"run,omitempty"`
		URL    string          `json:"url"`
	}
)

// AttachIntegration: POST /api/v1/projects/{slug}/integrations
// {capability, connector?, resource?}. errors + no resource → provision via
// the (named or default) errors connector. Anything else → validated direct
// entry against the capability's resource fields.
func (h ProjectAttachments) AttachIntegration(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	jsonCommand(w, r, jsonBody{errorText: "capability required"}, func(in struct {
		Capability string            `json:"capability"`
		Connector  string            `json:"connector"`
		Resource   map[string]string `json:"resource"`
	}) {
		if in.Capability == "" {
			WriteError(w, 400, "capability required")
			return
		}

		// Linked path for the errors capability: provision through the connector.
		if in.Capability == integration.CapErrors && len(in.Resource) == 0 {
			out, code, err := h.integrations.ProvisionErrors(r.Context(), p, in.Connector, Actor(r))
			if err != nil {
				WriteError(w, code, "%v", err)
				return
			}
			WriteJSON(w, 200, out)
			return
		}

		// Linked path for the feedback capability: register the app on the
		// Customer Feedback service and adopt its key.
		if in.Capability == integration.CapFeedback && len(in.Resource) == 0 {
			out, code, err := h.integrations.ProvisionFeedback(r.Context(), p, in.Connector, Actor(r))
			if err != nil {
				WriteError(w, code, "%v", err)
				return
			}
			WriteJSON(w, 200, out)
			return
		}

		// Linked path for the container-registry capability: mint a per-project
		// deploy token through the connector's provider (registry adapter).
		if in.Capability == integration.CapContainerRegistry && len(in.Resource) == 0 {
			out, code, err := h.integrations.ProvisionRegistry(r.Context(), p, in.Connector, Actor(r))
			if err != nil {
				WriteError(w, code, "%v", err)
				return
			}
			WriteJSON(w, 200, out)
			return
		}

		// AI models: the attachment is the project's choice of connector and
		// nothing else. Keys are made per deployment and per preview, when
		// they boot, so attaching calls no provider.
		if in.Capability == integration.CapLLM {
			if len(in.Resource) > 0 {
				WriteError(w, 400, "AI models take no resource fields: keys are made for each deployment and preview")
				return
			}
			name := in.Connector
			if name == "" {
				first, ok, err := h.integrations.Bindings.FirstName(r.Context(), integration.CapLLM)
				if err != nil {
					WriteFault(w, err)
					return
				}
				if !ok {
					WriteError(w, 422, "no AI models connector — an administrator adds the OpenRouter integration first")
					return
				}
				name = first
			} else if cfg, err := h.integrations.ConnectorConfig(r.Context(), name, integration.CapLLM); err != nil {
				WriteFaultOr(w, err, http.StatusServiceUnavailable)
				return
			} else if cfg == nil {
				WriteError(w, 422, "no AI models connector named %q", name)
				return
			}
			if err := h.integrations.Attachments.Attach(r.Context(), p.ID, integration.CapLLM, name, "{}", integration.Entered, Actor(r), map[string]any{"connector": name}); err != nil {
				WriteFault(w, err)
				return
			}
			WriteJSON(w, 200, AttachmentEntered{OK: true, Capability: integration.CapLLM, Connector: name, Provenance: "entered"})
			return
		}

		// Direct path: resource fields for the capability, entered.
		var fields []integration.Field
		switch {
		case in.Capability == integration.CapErrors:
			fields = []integration.Field{{Key: "dsn", Label: "DSN"}}
		case in.Capability == integration.CapFeedback:
			fields = []integration.Field{{Key: "appKey", Label: "App key"}, {Key: "url", Label: "Service URL"}}
		default:
			k, err := h.integrations.Definition(r.Context(), in.Capability)
			if err != nil {
				WriteFaultOr(w, err, http.StatusServiceUnavailable)
				return
			}
			if k == nil {
				WriteError(w, 404, "unknown capability %q — define it as a service kind first", in.Capability)
				return
			}
			fields = k.Resource.Fields
			if in.Connector != "" {
				cfg, err := h.integrations.ConnectorConfig(r.Context(), in.Connector, in.Capability)
				if err != nil {
					WriteFaultOr(w, err, http.StatusServiceUnavailable)
					return
				}
				if cfg == nil {
					WriteError(w, 422, "no %s connector named %q", in.Capability, in.Connector)
					return
				}
			}
		}
		resource := map[string]any{}
		for _, f := range fields {
			v := strings.TrimSpace(in.Resource[f.Key])
			if v == "" && !f.Optional {
				WriteError(w, 400, "resource field %q is required", f.Key)
				return
			}
			if v != "" {
				resource[f.Key] = v
			}
		}
		for k := range in.Resource {
			known := false
			for _, f := range fields {
				if f.Key == k {
					known = true
				}
			}
			if !known {
				WriteError(w, 400, "unknown resource field %q", k)
				return
			}
		}
		if err := h.integrations.Attachments.Attach(r.Context(), p.ID, in.Capability, in.Connector, encodedJSON(resource), integration.Entered, Actor(r), map[string]any{"fields": fieldKeys(resource)}); err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 200, AttachmentEntered{OK: true, Capability: in.Capability, Connector: in.Connector, Provenance: "entered"})
	})
}

// DetachIntegration: DELETE /api/v1/projects/{slug}/integrations/{capability}
// — stops the contributions and records the detach. Never deprovisions the
// provider-side resource: that is a destructive act deserving its own verb.
func (h ProjectAttachments) DetachIntegration(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	capability := r.PathValue("capability")
	if err := h.integrations.Attachments.Detach(r.Context(), p.ID, capability, Actor(r)); errors.Is(err, integration.ErrNotAttached) {
		WriteError(w, 404, "project has no %s integration", capability)
		return
	} else if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, Done{OK: true})
}

// ListProjectIntegrations: GET /api/v1/projects/{slug}/integrations —
// the project's integrations with provenance and (for errors) a live issue
// summary. Secret-bearing resource values are redacted; secretRef fields
// hold names, which are safe to show.
func (h ProjectAttachments) ListProjectIntegrations(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	attachments, err := integration.AttachedRows(r.Context(), h.db, p.ID)
	if err != nil {
		WriteFault(w, err)
		return
	}
	var out []AttachmentView
	for _, p := range attachments {
		x := AttachmentView{Capability: p.Capability, Connector: p.Binding, Provenance: p.Provenance,
			Resource: decodeBriefMap(p.ResourceJSON), CreatedAt: p.CreatedAt.UTC().Format(time.RFC3339Nano), Wirable: p.Capability != integration.CapContainerRegistry}
		if !p.WireChangeID.IsZero() {
			x.Wire = &IntegrationWire{ChangeID: p.WireChangeID, ChangeNumber: p.WireChangeNumber, State: p.WireState, Title: p.WireTitle}
		}
		out = append(out, x)
	}

	// Provider enrichment happens after the database read completes.
	for i := range out {
		if _, ok := out[i].Resource["dsn"]; ok {
			out[i].Resource["dsn"] = "••• (injected into app containers)"
		}
		if _, ok := out[i].Resource["token"]; ok {
			out[i].Resource["token"] = "••• (held by the engine; daemons are logged in for you)"
		}
		out[i].Docs = integrationDocPath(out[i].Capability)
		switch out[i].Capability {
		case integration.CapErrors:
			out[i].Title = "Error reporting"
			if out[i].Provenance == "provisioned" {
				h.integrations.ErrorsIssueSummary(r.Context(), p, out[i].Resource)
				if sum, ok := out[i].Resource["unresolved"].([]map[string]string); ok {
					for _, is := range sum {
						if is["lastSeen"] > out[i].LastEvent {
							out[i].LastEvent = is["lastSeen"]
						}
					}
				}
			}
		case integration.CapLLM:
			out[i].Title = "AI models"
		case integration.CapFeedback:
			out[i].Title = "Customer feedback"
			if k, ok := out[i].Resource["appKey"]; ok && k != "" {
				out[i].Resource["appKey"] = "••• (injected into app containers and agent steering)"
			}
		default:
			if k, _ := integration.ParseDefinition(attachments[i].KindYAML); k != nil {
				out[i].Title = k.Title
			} else {
				out[i].Title = out[i].Capability
			}
		}
	}
	if out == nil {
		out = []AttachmentView{}
	}
	WriteJSON(w, 200, AttachmentList{Attachments: out})
}

// ListAttachables: GET /api/v1/attachables — what the pickers offer:
// each attachable capability with its connectors, whether AppTown can
// provision (linked path), and the fields the direct path needs.
func (h ProjectAttachments) ListAttachables(w http.ResponseWriter, r *http.Request) {
	out := []Attachable{}
	errCons, err := h.connectorNames(r.Context(), integration.CapErrors)
	if err != nil {
		WriteFault(w, err)
		return
	}
	out = append(out, Attachable{
		Capability: integration.CapErrors, Title: "Error reporting",
		Description:   "Know when the app breaks for a real person: every uncaught error, server or browser, lands in one place with its stack trace, grouped and counted.",
		Provisionable: len(errCons) > 0, Connectors: errCons,
		ResourceFields: []integration.Field{{Key: "dsn", Label: "DSN", Help: "Direct path: paste an existing project DSN."}},
	})
	fbCons, err := h.connectorNames(r.Context(), integration.CapFeedback)
	if err != nil {
		WriteFault(w, err)
		return
	}
	out = append(out, Attachable{
		Capability: integration.CapFeedback, Title: "Customer feedback",
		Description:   "A feedback button in the app, so the people using it can tell you what is wrong or what they wish for — collected in one place instead of lost in chat.",
		Provisionable: len(fbCons) > 0, Connectors: fbCons,
		ResourceFields: []integration.Field{{Key: "appKey", Label: "App key", Help: "Direct path: paste an existing app key."}, {Key: "url", Label: "Service URL"}},
	})
	llmCons, err := h.connectorNames(r.Context(), integration.CapLLM)
	if err != nil {
		WriteFault(w, err)
		return
	}
	out = append(out, Attachable{
		Capability: integration.CapLLM, Title: "AI models",
		Description:   "Lets the app call AI models through OpenRouter. Each deployment and each preview of an app that declares the llm need gets its own key with a spending limit, revoked when it is taken down or ends.",
		Provisionable: len(llmCons) > 0, Connectors: llmCons, ResourceFields: []integration.Field{},
	})
	kinds, err := h.integrations.Definitions(r.Context())
	if err != nil {
		WriteFaultOr(w, err, http.StatusServiceUnavailable)
		return
	}
	for _, k := range kinds {
		// A kind whose resource fields are all optional needs no input:
		// attachable in one click (wizard included) — nothing is minted,
		// the contributions just switch on.
		oneClick := true
		for _, f := range k.Resource.Fields {
			if !f.Optional {
				oneClick = false
			}
		}
		connectors, err := h.connectorNames(r.Context(), k.Capability)
		if err != nil {
			WriteFault(w, err)
			return
		}
		out = append(out, Attachable{
			Capability: k.Capability, Title: k.Title, Description: k.Description,
			Provisionable: oneClick, Connectors: connectors,
			ResourceFields: k.Resource.Fields,
		})
	}
	WriteJSON(w, 200, AttachableList{Attachables: out})
}

// WireIntegration: POST /api/v1/projects/{slug}/integrations/{capability}/wire
// body {mode: "workflow"|"chat"} → {change, run?, url}
func (h ProjectAttachments) WireIntegration(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	if _, ok := AuthorizeRequest(h.access, w, r, access.CapProjectIntegrations, project.ObservedResource(r.Context(), h.projects, p), access.Context{}); !ok {
		return
	}
	cap := r.PathValue("capability")
	if cap == integration.CapContainerRegistry {
		WriteError(w, 422, "a container registry is used by AppTown's deploy, not by the app — there is nothing to wire")
		return
	}
	attached, err := h.integrations.Attachments.IsAttached(r.Context(), p.ID, cap)
	if err != nil {
		WriteFault(w, err)
		return
	}
	if !attached {
		WriteError(w, 404, "%s is not attached to this project", cap)
		return
	}
	jsonCommand(w, r, jsonBody{optional: true}, func(in struct {
		Mode string `json:"mode"`
	}) {
		mode, err := changeactions.ParseWiringMode(in.Mode)
		if err != nil {
			WriteError(w, 400, "%v", err)
			return
		}

		title, err := h.attachmentTitle(r.Context(), cap)
		if err != nil {
			WriteFaultOr(w, err, http.StatusServiceUnavailable)
			return
		}
		body := fmt.Sprintf(`This project has been attached to %s (%s). Wire the app into it.

Your steering has an "(attached, generated)" section for this service that
says exactly which env vars are injected and what the code must do with
them. Follow it precisely and minimally: initialise from the injected env,
never hardcode endpoints/DSNs/keys, and keep the app running cleanly when
those env vars are absent (local dev). Verify both modes.`, title, cap)
		// One command admits, links and records the wiring; a failed link leaves
		// nothing behind, and a live wiring answers 409 naming its Change.
		ch, run, err := h.actions.OpenWiring(r.Context(), changeactions.Wiring{
			Mode: mode, Capability: cap, Workflow: wireWorkflow, Who: Actor(r),
			Draft: workflow.ChangeDraft{ProjectID: p.ID, Type: "feature", Title: "Connect " + title, Body: body},
		})
		if err != nil {
			WriteFaultOr(w, err, 422)
			return
		}
		WriteJSON(w, 200, IntegrationWiring{Change: *ch, Run: run, URL: changeactions.URL(h.publicHost, ch.ID)})
	})
}

func (h ProjectAttachments) attachmentTitle(ctx context.Context, capability string) (string, error) {
	switch capability {
	case integration.CapErrors:
		return "Error reporting", nil
	case integration.CapFeedback:
		return "Customer feedback", nil
	case integration.CapLLM:
		return "AI models", nil
	}
	k, err := h.integrations.Definition(ctx, capability)
	if err != nil || k == nil {
		return capability, err
	}
	return k.Title, nil
}
func fieldKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// integrationDocPath: the developer guide for a capability, when the
// stock docs carry one — linked from the attachment card so the test
// hooks and the wiring rules are one click from where you attached it.
func integrationDocPath(capability string) string {
	for _, c := range integration.ConnectorCatalog() {
		if c.Capability != capability {
			continue
		}
		if _, err := docs.FS.ReadFile("integrations/" + c.Capability + "-" + c.Provider + ".developer.md"); err == nil {
			return "/docs/" + c.Capability + "/" + c.Provider
		}
	}
	return ""
}

type IntegrationWire struct {
	ChangeID     ids.ID `json:"changeId"`
	ChangeNumber int64  `json:"changeNumber"` // within the project
	State        string `json:"state"`
	Title        string `json:"title"`
}

// connectorNames is never nil: the picker reads it as a list.
func (h ProjectAttachments) connectorNames(ctx context.Context, capability string) ([]string, error) {
	names, err := h.integrations.Bindings.Names(ctx, capability)
	if err != nil {
		return nil, err
	}
	if names == nil {
		return []string{}, nil
	}
	return names, nil
}

const wireWorkflow = "adopt-integration"
