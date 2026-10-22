package httpapi

import (
	"context"
	"net/http"
	"slices"
	"sort"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/changeactions"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/database"
)

type CatalogChanges struct {
	ways     *wow.Service
	adoption *adoption.Service
	projects *project.Projects
	access   *identity.Service
	actions  *changeactions.Service
	authMode string
}

func NewCatalogChanges(ways *wow.Service, adoption *adoption.Service, projects *project.Projects, access *identity.Service, actions *changeactions.Service, authMode string) CatalogChanges {
	return CatalogChanges{ways: ways, adoption: adoption, projects: projects, access: access, actions: actions, authMode: authMode}
}

// CatalogList: GET /catalog
func (h CatalogChanges) CatalogList(w http.ResponseWriter, r *http.Request) {
	changes, err := h.ways.CatalogChanges(r.Context())
	if err != nil {
		WriteFault(w, err)
		return
	}
	var proj *project.Project
	if slug := r.URL.Query().Get("project"); slug != "" {
		proj, _ = h.projectBySlug(r.Context(), slug)
	}
	// Collect first, annotate after: nested queries while this result set
	// is open misbehave on the small pool (store.go's standing warning —
	// re-learned when a freshly installed workflow read as missing).
	out := []CatalogEntry{}
	for _, c := range changes {
		out = append(out, CatalogEntry{c, c.State().String(), "", ""})
	}
	if proj != nil {
		own := ""
		state, err := h.adoption.Current(r.Context(), proj.ID)
		if found, err := database.Lookup(err); err != nil {
			WriteError(w, 500, "reading the project's adoption: %v", err)
			return
		} else if found {
			own = state.Pack
		}
		var ownWorkflows []string
		if own != "" {
			pk, err := packNamed(r.Context(), h.ways, own)
			if err != nil {
				WriteFault(w, err)
				return
			}
			if pk != nil {
				ownWorkflows = pk.Workflows
			}
		}
		// What the project can run: its selected lanes plus the system
		// workflows. Read once, and a failed read fails the list, so a
		// failure never reads as "this workflow is missing".
		available, err := h.adoption.AvailableWorkflows(r.Context(), proj.ID)
		if err != nil {
			WriteFault(w, err)
			return
		}
		runnable := map[string]bool{}
		for _, e := range available {
			runnable[e.Name] = true
		}
		for i := range out {
			def, err := h.ways.CatalogDefinition(r.Context(), out[i].ID, out[i].LatestVersion)
			if err != nil {
				continue
			}
			wf := def.Workflow
			if wf == "" || runnable[wf] {
				continue
			}
			out[i].MissingWorkflow = wf
			// Only the project's OWN pack is offered for a one-click pull —
			// suggesting whichever pack happens to carry the workflow nearly
			// re-packed a Java project as django-htmx. A foreign pack
			// carrying it is information, not an invitation.
			if slices.Contains(ownWorkflows, wf) {
				out[i].FromPack = own
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	WriteJSON(w, 200, CatalogList{Catalog: out})
}

// CatalogApply: POST /catalog/{name...}/apply {project, params} — any
// member. Drafts may be applied single-project (that is how they earn
// receipts); the catalog listing is what promotion gates, not invocation.
func (h CatalogChanges) CatalogApply(w http.ResponseWriter, r *http.Request) {
	c, err := h.ways.CatalogChange(r.Context(), r.PathValue("name"))
	if err != nil {
		WriteError(w, 404, "%v", err)
		return
	}
	jsonCommand(w, r, jsonBody{}, func(in struct {
		Project string            `json:"project"`
		Params  map[string]string `json:"params"`
	}) {
		p, err := h.projectBySlug(r.Context(), in.Project)
		if err != nil {
			writeReadFault(w, err, "no such project %q", in.Project)
			return
		}
		if !RequireProjectMember(h.access, h.authMode, w, r, p.ID, p.Slug) {
			return
		}
		item, run, err := h.actions.ApplyCatalog(r.Context(), p, c, in.Params, Actor(r))
		if err != nil {
			WriteError(w, 422, "%v", err)
			return
		}
		WriteJSON(w, 201, CatalogApplied{Change: *item, Run: *run, Catalog: c.Name, CatalogStatus: c.State().String()})
	})
}

func (h CatalogChanges) projectBySlug(ctx context.Context, slug string) (*project.Project, error) {
	p, err := h.projects.BySlug(ctx, slug)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// CatalogEntry is one catalog change with its status and, for a project,
// whether it can run there.
type CatalogEntry struct {
	wow.CatalogChange
	Status string `json:"status"`
	// Per-project fit (?project=slug): a change whose workflow the
	// project lacks is offered grayed, pointing at the pack that
	// carries it — never a red error after the click.
	MissingWorkflow string `json:"missingWorkflow,omitempty"`
	FromPack        string `json:"fromPack,omitempty"`
}

// CatalogList is the change catalog.
type CatalogList struct {
	Catalog []CatalogEntry `json:"catalog"`
}

// CatalogApplied is the change and run a catalog change started.
type CatalogApplied struct {
	Change        workflow.Change `json:"change"`
	Run           workflow.Run    `json:"run"`
	Catalog       string          `json:"catalog"`
	CatalogStatus string          `json:"catalogStatus"`
}
