package httpapi

import (
	"net/http"
	"strings"
	"time"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/deployment"
	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/workflow"
)

type ProjectTransfer struct {
	projects   *project.Projects
	access     *identity.Service
	work       *workflow.Service
	deployment *deployment.Service
	db         *database.DB
	now        func() time.Time // the forge's clock, which stamps its receipts; nil is the real one
}

func NewProjectTransfer(projects *project.Projects, access *identity.Service, work *workflow.Service, deployment *deployment.Service, db *database.DB, now func() time.Time) ProjectTransfer {
	return ProjectTransfer{projects: projects, access: access, work: work, deployment: deployment, db: db, now: now}
}

// TransferProject: POST /api/v1/projects/{slug}/owner {group} — one
// click for a YOUNG project (no changes, no deployments); a grown project's
// transfer is the consequential flow the design describes, not built yet.
func (h ProjectTransfer) TransferProject(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	jsonCommand(w, r, jsonBody{errorText: "group required"}, func(in struct{ Group string }) {
		if strings.TrimSpace(in.Group) == "" {
			WriteError(w, 400, "group required")
			return
		}

		if kind, err := h.access.GroupKind(r.Context(), in.Group); err != nil {
			WriteFault(w, err)
			return
		} else if kind == "" {
			WriteError(w, 404, "no such group %q", in.Group)
			return
		}
		activity, err := h.work.ProjectActivity(r.Context(), p.ID)
		if err != nil {
			WriteFault(w, err)
			return
		}
		changes, envs := activity[p.ID].Changes, 0
		if placed, err := h.deployment.Deployments.ForProject(r.Context(), p.ID); err != nil {
			WriteFault(w, err)
			return
		} else {
			envs = len(placed)
		}
		if changes > 0 || envs > 0 {
			WriteError(w, 409, "%s has history (%d changes, %d deployments); moving a grown project is a bigger act than this button, and is not built yet", p.Name, changes, envs)
			return
		}
		// Both sides must agree: manage the project, and be able to create in the
		// target group.
		if _, ok := AuthorizeRequest(h.access, w, r, access.CapProjectManageMembers, project.ObservedResource(r.Context(), h.projects, p), access.Context{}); !ok {
			return
		}
		if _, ok := AuthorizeRequest(h.access, w, r, access.CapProjectCreate, access.Resource{Type: "group", ID: in.Group}, access.Context{}); !ok {
			return
		}
		from := p.OwnerGroup
		if err := h.projects.SetOwnerGroup(r.Context(), p.ID, in.Group); err != nil {
			WriteError(w, 500, "transferring the project: %v", err)
			return
		}
		audit.At(clock.Or(h.now)()).BestEffort(r.Context(), h.db, Actor(r), p.ID, "project.transferred", p.Slug, map[string]any{"from": from, "to": in.Group})
		WriteJSON(w, 200, ProjectTransferred{OK: true, From: from, To: in.Group})
	})
}

// ProjectTransferred names the group a project moved from and to.
type ProjectTransferred struct {
	OK   bool   `json:"ok"`
	From string `json:"from"`
	To   string `json:"to"`
}
