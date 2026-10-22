package httpapi

import (
	"net/http"
	"slices"
	"sort"
	"time"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy/docker"
)

type Extensions struct {
	projects   *project.Projects
	adoption   *adoption.Service
	access     *identity.Service
	docker     *docker.Scope
	catalog    []CuratedExtension
	agentImage string
	authMode   string
	db         *database.DB
	now        func() time.Time // AppTown's clock, which stamps its receipts; nil is the real one
}

func NewExtensions(projects *project.Projects, adoption *adoption.Service, access *identity.Service, docker *docker.Scope, catalog []CuratedExtension, agentImage string, authMode string, db *database.DB, now func() time.Time) Extensions {
	return Extensions{projects: projects, adoption: adoption, access: access, docker: docker, catalog: slices.Clone(catalog), agentImage: agentImage, authMode: authMode, db: db, now: now}
}

// ListExtensions: GET /projects/{slug}/extensions — the blessed optional
// catalog with each one's enabled state for this project.
func (h Extensions) ListExtensions(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	enabled, err := h.adoption.EnabledExtensions(r.Context(), p.ID)
	if err != nil {
		WriteFault(w, err)
		return
	}
	on := map[string]bool{}
	for _, n := range enabled {
		on[n] = true
	}
	out := []ExtensionRow{}
	for _, e := range h.catalog {
		out = append(out, ExtensionRow{CuratedExtension: e, Enabled: on[e.Name], Version: h.docker.AgentImageLabel(r.Context(), h.agentImage, e.Label)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	WriteJSON(w, 200, ExtensionList{Extensions: out})
}

// SetExtension: POST /projects/{slug}/extensions {name, enabled}.
func (h Extensions) SetExtension(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	if !RequireProjectMember(h.access, h.authMode, w, r, p.ID, p.Slug) {
		return
	}
	jsonCommand(w, r, jsonBody{}, func(in struct {
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
	}) {
		if !slices.ContainsFunc(h.catalog, func(e CuratedExtension) bool { return e.Name == in.Name }) {
			WriteError(w, 422, "%q is not a blessed extension — AppTown only enables baked, curated extensions", in.Name)
			return
		}
		if err := h.adoption.SetExtension(r.Context(), p.ID, in.Name, in.Enabled); err != nil {
			WriteFault(w, err)
			return
		}
		audit.At(clock.Or(h.now)()).BestEffort(r.Context(), h.db, Actor(r), p.ID, "project.extension."+map[bool]string{true: "enabled", false: "disabled"}[in.Enabled], in.Name, nil)
		WriteJSON(w, 200, Done{OK: true})
	})
}

// CuratedExtension is a copy of the baked runtime catalog for display and selection.
type CuratedExtension struct {
	Name        string `json:"name"`
	Package     string `json:"package"`
	Label       string `json:"-"`
	Description string `json:"description"`
}

// ExtensionRow is a curated extension and whether this project runs it.
type ExtensionRow struct {
	CuratedExtension
	Enabled bool   `json:"enabled"`
	Version string `json:"version"`
}

// ExtensionList is the curated extension catalog for a project.
type ExtensionList struct {
	Extensions []ExtensionRow `json:"extensions"`
}
