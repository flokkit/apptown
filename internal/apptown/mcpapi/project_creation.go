package mcpapi

import (
	"errors"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/projectlifecycle"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/database"
)

type ProjectCreationDependencies struct {
	Scope     Scope
	Lifecycle *projectlifecycle.Service
	DB        *database.DB
	// Now is AppTown's clock, which stamps the tool's receipt (the real
	// clock when nil).
	Now func() time.Time
}
type projectCreation struct {
	scope     Scope
	lifecycle *projectlifecycle.Service
	db        *database.DB
	now       func() time.Time
}

func NewProjectCreation(deps ProjectCreationDependencies) []Tool {
	h := projectCreation{scope: deps.Scope, lifecycle: deps.Lifecycle, db: deps.DB, now: deps.Now}
	return []Tool{{Name: "create_project",
		order: 21, Description: "Creates a new project in your name: a slug, a name, a one-sentence blurb, and a pack (the way of working, e.g. \"html-game\", \"django-htmx\"; list_packs is not a tool — get_project on any project names its pack, and AppTown's Packs page lists them). " + "The repository is initialised and the pack's starter files land as the first commit. Then open a Change on it with create_change.", InputSchema: schemaObject(map[string]any{"slug": schemaString("Short URL name: lowercase letters, digits and dashes, e.g. \"snake-game\"."), "name": schemaString("The project's name as people say it."), "blurb": schemaString("One sentence: what this is for. It becomes the project's remembered intent."),
			"pack":  schemaString("The way of working to adopt, e.g. \"html-game\"."),
			"group": schemaString("Optional: the group that owns it; default is your own default group."),
		}, "slug", "name"),
		command: ownerCommand("project_creation.createProject", h.createProject),
	},
	}
}
func (h projectCreation) createProject(c *Call, args map[string]any) (any, error) {
	pr := c.principal()
	group, err := h.scope.authority.OwningGroupFor(c.Context, pr, argString(args, "group"))
	if err != nil {
		return nil, refuse("%v", err)
	}
	if err := h.scope.authorize(c, "create_project", access.CapProjectCreate, access.Resource{Type: "group", ID: group}); err != nil {
		return nil, err
	}
	packName := argString(args, "pack")
	p, err := h.lifecycle.Create(c.Context, projectlifecycle.Draft{Slug: argString(args, "slug"), Name: argString(args, "name"), Blurb: argString(args, "blurb"), Group: group, Actor: c.actor(), Pack: packName})
	if errors.Is(err, adoption.ErrPackNotOffered) {
		return nil, refuse("no pack %q is offered to new projects; get_project on a similar project names its pack", packName)
	}
	if err != nil {
		return nil, refuse("%v", err)
	}
	var pack *wow.Pack
	if packName != "" {
		if pack, err = h.lifecycle.OfferedPack(c.Context, packName); err != nil {
			return nil, refuse("the project exists but its pack could not be read: %v", err)
		}
	}
	if err := h.lifecycle.InitializeRepository(c.Context, p, "", ""); err != nil {
		return nil, refuse("the project exists but its repository could not be initialised: %v", err)
	}
	audit.At(clock.Or(h.now)()).BestEffort(c.Context, h.db, c.actor(), p.ID, "project.repo.initialized", p.Slug, map[string]any{"defaultBranch": p.DefaultBranch, "via": "mcp"})
	out := map[string]any{"slug": p.Slug, "name": p.Name, "ownerGroup": group, "defaultBranch": p.DefaultBranch,
		"webUrl": c.projectURL(p.Slug), "next": "create_change on it with a lane (e.g. implement) and a clear brief"}
	if pack != nil {
		p, _ = h.scope.bySlug(c.Context, p.Slug)
		// The pack is part of the creation admitted above: the new project's
		// first way of working, an offered pack, on a project nobody has
		// adopted anything on yet. It is applied with authority scoped to
		// that one project, so an agent that may create projects gets the
		// pack it asked for without being able to change any project's way
		// of working afterwards (project:configure-way-of-working is in no
		// agent scope).
		res, err := h.lifecycle.ApplyPack(adoption.WithSystemAuthority(c.Context, p.ID), p, pack, c.actor())
		if err != nil {
			out["pack"] = map[string]any{"name": pack.Name, "error": err.Error()}
		} else {
			out["pack"] = map[string]any{"name": pack.Name, "lanes": res.WorkflowsInstalled, "seeded": res.Seeded, "missing": res.Missing}
		}
	}
	return out, nil
}
