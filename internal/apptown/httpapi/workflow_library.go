package httpapi

import (
	"net/http"
	"strings"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/wow"
)

type WorkflowLibrary struct {
	ways     *wow.Service
	access   *identity.Service
	projects *project.Projects
}

func NewWorkflowLibrary(ways *wow.Service, access *identity.Service, projects *project.Projects) WorkflowLibrary {
	return WorkflowLibrary{ways: ways, access: access, projects: projects}
}

// PutLibraryWorkflow: POST /workflows/library {yaml} — author or edit a
// CUSTOM instance-level workflow. Creates v1, or bumps a new version if a custom
// one of that name already exists. A stock (AppTown-shipped) name is read-only:
// re-authoring it would be clobbered by the next release, so it is refused —
// author under your own name instead.
func (h WorkflowLibrary) PutLibraryWorkflow(w http.ResponseWriter, r *http.Request) {
	if _, ok := AuthorizeRequest(h.access, w, r, access.CapExecWorkflows, access.Resource{Type: "instance"}, access.Context{}); !ok {
		return
	}
	jsonCommand(w, r, jsonBody{errorText: "yaml required"}, func(in struct {
		YAML string `json:"yaml"`
	}) {
		if strings.TrimSpace(in.YAML) == "" {
			WriteError(w, 400, "yaml required")
			return
		}

		doc, err := schema.ParseWorkflow([]byte(in.YAML))
		if err != nil {
			WriteError(w, 400, "%v", err)
			return
		}
		if doc.Metadata.Name == "" {
			WriteError(w, 400, "workflow metadata.name is required")
			return
		}
		name, version, err := h.ways.PublishWorkflow(r.Context(), in.YAML, Actor(r))
		if err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 200, WorkflowPublished{Name: name, Version: version})
	})
}

// DeleteLibraryWorkflow: DELETE /workflows/library/{name} — remove a custom
// instance-level workflow. Stock workflows are read-only. Project copies already
// installed are untouched.
func (h WorkflowLibrary) DeleteLibraryWorkflow(w http.ResponseWriter, r *http.Request) {
	if _, ok := AuthorizeRequest(h.access, w, r, access.CapExecWorkflows, access.Resource{Type: "instance"}, access.Context{}); !ok {
		return
	}
	name := r.PathValue("name")
	if err := h.ways.DeleteWorkflow(r.Context(), name, Actor(r)); err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, Done{OK: true})
}

// WorkflowHistory: GET /projects/{slug}/workflows/{name}/history — the
// library's versions of the workflow, newest first. A project holds no
// copies, so its workflow's history is the library's.
func (h WorkflowLibrary) WorkflowHistory(w http.ResponseWriter, r *http.Request) {
	if _, ok := ProjectFromPath(h.projects, w, r); !ok {
		return
	}
	out, err := h.ways.WorkflowHistory(r.Context(), r.PathValue("name"))
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, out)
}

// WorkflowPublished names the workflow version a publish wrote.
type WorkflowPublished struct {
	Name    string `json:"name"`
	Version int    `json:"version"`
}
