package httpapi

import (
	"net/http"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/database"
)

type ProjectPolicies struct {
	projects *project.Projects
	access   *identity.Service
	ways     *wow.Service
	adoption *adoption.Service
	db       *database.DB
}

func NewProjectPolicies(projects *project.Projects, access *identity.Service, ways *wow.Service, adoption *adoption.Service, db *database.DB) ProjectPolicies {
	return ProjectPolicies{projects: projects, access: access, ways: ways, adoption: adoption, db: db}
}

// ListPolicies: GET /projects/{slug}/policies — policies plus the live
// conformance matrix.
func (h ProjectPolicies) ListPolicies(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	policies, err := h.adoption.ProjectPolicies(r.Context(), h.db, p.ID)
	if err != nil {
		WriteFault(w, err)
		return
	}
	if policies == nil {
		policies = []adoption.Policy{}
	}
	violations, err := h.adoption.ProjectConformance(r.Context(), h.db, p.ID)
	if err != nil {
		WriteFault(w, err)
		return
	}
	if violations == nil {
		violations = []adoption.Violation{}
	}
	WriteJSON(w, 200, ProjectPolicyList{Policies: policies, Violations: violations})
}

// CreatePolicy: POST /projects/{slug}/policies
func (h ProjectPolicies) CreatePolicy(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	authorityResource, err := h.projects.AuthorityResource(r.Context(), p.ID)
	if err != nil {
		WriteFault(w, err)
		return
	}
	if _, ok := AuthorizeRequest(h.access, w, r, access.CapProjectWayOfWorking, authorityResource, access.Context{}); !ok {
		return
	}
	jsonCommand(w, r, jsonBody{}, func(in struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Rule        string         `json:"rule"`
		With        map[string]any `json:"with"`
		FromLibrary string         `json:"fromLibrary"`
	}) {
		if in.FromLibrary != "" {
			lp := h.ways.LibraryPolicy(r.Context(), in.FromLibrary)
			if lp == nil {
				WriteError(w, 404, "no library policy %q", in.FromLibrary)
				return
			}
			in.Name, in.Description, in.Rule, in.With = lp.Name, lp.Description, lp.Rule, lp.With
		}
		if in.Name == "" || in.Rule == "" {
			WriteError(w, 400, "name and rule required (or fromLibrary)")
			return
		}
		origin := ""
		if in.FromLibrary != "" {
			origin = "library/" + in.FromLibrary
		}
		if err := h.adoption.InstallPolicy(r.Context(), p.ID, in.Name, in.Description, in.Rule, in.With, origin, Actor(r)); err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 201, Done{OK: true})
	})
}

// DeletePolicy: DELETE /policies/{id} — the audited escape hatch.
func (h ProjectPolicies) DeletePolicy(w http.ResponseWriter, r *http.Request) {
	if _, ok := AuthorizeRequest(h.access, w, r, access.CapStandardsPolicies, access.Resource{Type: "instance"}, access.Context{}); !ok {
		return
	}
	id, err := PathID(r, "id")
	if err != nil {
		WriteError(w, 400, "bad id")
		return
	}
	if err := h.adoption.RemovePolicy(r.Context(), id, Actor(r)); err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, Done{OK: true})
}

// ProjectPolicyList is a project's policies and what currently breaks them.
type ProjectPolicyList struct {
	Policies   []adoption.Policy    `json:"policies"`
	Violations []adoption.Violation `json:"violations"`
}
