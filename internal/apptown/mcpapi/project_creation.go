package mcpapi

import (
	"errors"
	"strings"
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
	Scope     scope
	Lifecycle *projectlifecycle.Service
	DB        *database.DB
	// Now is AppTown's clock, which stamps the tool's receipt (the real
	// clock when nil).
	Now func() time.Time
}
type projectCreation struct {
	scope     scope
	lifecycle *projectlifecycle.Service
	db        *database.DB
	now       func() time.Time
}

func NewProjectCreation(deps ProjectCreationDependencies) []toolEntry {
	h := projectCreation{scope: deps.Scope, lifecycle: deps.Lifecycle, db: deps.DB, now: deps.Now}
	return []toolEntry{{Name: "create_project",
		order: 21, Description: "Creates a new project in your name: a slug, a name, a request in the asker's words, and its packs (its way of working: a server app takes server-base, one backend such as \"go\" or \"django\", \"postgres\" if it keeps records, and a UI pack such as \"vite-react\" or \"server-rendered\"; a standalone pack such as \"html-game\" is taken alone. list_packs is not a tool — get_project on any project names its packs, and AppTown's Packs page lists them). " + "The repository is initialised and the packs' starter files land as the first commit. Then open a Change on it with create_change.", InputSchema: schemaObject(map[string]any{"slug": schemaString("Short URL name: lowercase letters, digits and dashes, e.g. \"snake-game\"."), "name": schemaString("The project's name as people say it."), "blurb": schemaString("What this project is for, in the asker's words; AppTown writes the short description from it."),
			"packs": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "The packs to adopt together, e.g. [\"server-base\", \"go\", \"postgres\", \"vite-react\"] or [\"html-game\"]."},
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
	var packNames []string
	if raw, ok := args["packs"].([]any); ok {
		for _, v := range raw {
			name, ok := v.(string)
			if !ok || strings.TrimSpace(name) == "" {
				return nil, refuse("packs must be pack names")
			}
			packNames = append(packNames, strings.TrimSpace(name))
		}
	} else if args["packs"] != nil {
		return nil, refuse("packs must be a list of pack names")
	}
	request, _ := args["blurb"].(string)
	p, err := h.lifecycle.Create(c.Context, projectlifecycle.Draft{Slug: argString(args, "slug"), Name: argString(args, "name"), Blurb: request, Group: group, Actor: c.actor(), Packs: packNames})
	if errors.Is(err, adoption.ErrPackNotOffered) {
		return nil, refuse("%v; get_project on a similar project names its packs", err)
	}
	if err != nil {
		return nil, refuse("%v", err)
	}
	var packs []*wow.Pack
	if len(packNames) > 0 {
		if packs, err = h.lifecycle.OfferedPacks(c.Context, packNames); err != nil {
			return nil, refuse("the project exists but its packs could not be read: %v", err)
		}
	}
	if err := h.lifecycle.InitializeRepository(c.Context, p, "", ""); err != nil {
		return nil, refuse("the project exists but its repository could not be initialised: %v", err)
	}
	audit.At(clock.Or(h.now)()).BestEffort(c.Context, h.db, c.actor(), p.ID, "project.repo.initialized", p.Slug, map[string]any{"defaultBranch": p.DefaultBranch, "via": "mcp"})
	out := map[string]any{"slug": p.Slug, "name": p.Name, "ownerGroup": group, "defaultBranch": p.DefaultBranch,
		"webUrl": c.projectURL(p.Slug), "next": "create_change on it with a lane (e.g. implement) and a clear brief"}
	if len(packs) > 0 {
		p, _ = h.scope.bySlug(c.Context, p.Slug)
		// The packs are part of the creation admitted above: the new project's
		// first way of working, offered packs, on a project nobody has
		// adopted anything on yet. It is applied with authority scoped to
		// that one project, so an agent that may create projects gets the
		// pack it asked for without being able to change any project's way
		// of working afterwards (project:configure-way-of-working is in no
		// agent scope).
		res, err := h.lifecycle.ApplyPack(adoption.WithSystemAuthority(c.Context, p.ID), p, packs, c.actor())
		if err != nil {
			out["packs"] = map[string]any{"names": packNames, "error": err.Error()}
		} else {
			out["packs"] = map[string]any{"names": res.Packs, "lanes": res.WorkflowsInstalled, "seeded": res.Seeded, "missing": res.Missing}
		}
	}
	return out, nil
}
