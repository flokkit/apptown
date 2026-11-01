package httpapi

import (
	"net/http"
	"strings"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/projectlifecycle"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/database"
)

type WorkflowSelection struct {
	projects  *project.Projects
	access    *identity.Service
	adoption  *adoption.Service
	ways      *wow.Service
	lifecycle *projectlifecycle.Service
	db        *database.DB
}

func NewWorkflowSelection(projects *project.Projects, access *identity.Service, adoption *adoption.Service, ways *wow.Service, lifecycle *projectlifecycle.Service, db *database.DB) WorkflowSelection {
	return WorkflowSelection{projects: projects, access: access, adoption: adoption, ways: ways, lifecycle: lifecycle, db: db}
}

// PutProjectLane: PUT /api/v1/projects/{slug}/lanes {name, enabled}
func (h WorkflowSelection) PutProjectLane(w http.ResponseWriter, r *http.Request) {
	p, ok := projectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	authorityResource, err := h.projects.AuthorityResource(r.Context(), p.ID)
	if err != nil {
		WriteFault(w, err)
		return
	}
	if _, ok := AuthorizeRequest(h.access, w, r, access.CapProjectWorkflow, authorityResource, access.Context{}); !ok {
		return
	}
	jsonCommand(w, r, jsonBody{errorText: "name required"}, func(in struct {
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
	}) {
		if in.Name == "" {
			WriteError(w, 400, "name required")
			return
		}

		_, yamlSrc, err := h.ways.WorkflowVersion(r.Context(), in.Name, 0)
		if err != nil {
			WriteError(w, 404, "%v", err)
			return
		}
		doc, err := schema.ParseWorkflowStored([]byte(yamlSrc))
		if err != nil {
			WriteError(w, 422, "%v", err)
			return
		}
		if doc.Metadata.System {
			WriteError(w, 422, "%q is a system workflow — AppTown runs it for every project; nothing to select", in.Name)
			return
		}
		// A deprecated workflow stays runnable where it is already selected,
		// so old runs read and can finish; nothing new turns it on. Its
		// description names what replaced it.
		deprecated := doc.Metadata.Deprecated //lint:ignore SA1019 reading the deprecated flag is the point
		if in.Enabled && deprecated {
			WriteError(w, 422, "%q is deprecated and cannot be turned on: %s", in.Name, strings.TrimSpace(doc.Metadata.Description))
			return
		}
		if in.Enabled {
			vs, err := h.adoption.CheckWorkflow(r.Context(), h.db, p.ID, in.Name, &doc.Spec)
			if err != nil {
				WriteFault(w, err)
				return
			}
			if len(vs) > 0 {
				WriteError(w, 422, "policy violation: %s", adoption.FormatViolations(vs))
				return
			}
		}
		if err := h.lifecycle.SelectLane(r.Context(), p.ID, in.Name, in.Enabled, actor(r)); err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 200, Done{OK: true})
	})
}
