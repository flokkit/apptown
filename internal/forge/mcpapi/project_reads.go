package mcpapi

import (
	"context"
	"path/filepath"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/deploy/appcontract"
	"flokkit.io/forge/internal/forge/adoption"
	"flokkit.io/forge/internal/forge/conformance"
	"flokkit.io/forge/internal/forge/deployment"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/forge/wow"
	"flokkit.io/forge/internal/gitexec"
	"flokkit.io/forge/internal/ids"
)

type ProjectReadsDependencies struct {
	Scope       Scope
	Work        *workflow.Service
	Adoption    *adoption.Service
	Ways        *wow.Service
	Conformance *conformance.Service
	Deployments *deployment.Service
	DB          *database.DB
	DataDir     string
}
type projectReads struct {
	scope       Scope
	work        *workflow.Service
	adoption    *adoption.Service
	ways        *wow.Service
	conformance *conformance.Service
	deployments *deployment.Service
	db          *database.DB
	dataDir     string
}

func NewProjectReads(deps ProjectReadsDependencies) []Tool {
	h := projectReads{scope: deps.Scope, work: deps.Work, adoption: deps.Adoption, ways: deps.Ways, conformance: deps.Conformance, deployments: deps.Deployments, db: deps.DB, dataDir: deps.DataDir}
	return []Tool{{
		Name:        "list_projects",
		order:       0,
		Description: "Returns the projects you can see, each with its slug, name, way of working, default branch, the URL of its main deployment if one is deployed, and how many Changes are open.",
		InputSchema: schemaObject(nil),
		command:     filteredCommand("project_reads.listProjects", h.listProjects),
	},
		{
			Name:        "get_project",
			order:       1,
			Description: "Returns one project: its lanes (the workflows it can run), the guidance it has adopted, its latest conformance standing, and its deployments.",
			InputSchema: schemaObject(map[string]any{"slug": slugArg}, "slug"),
			command:     ownerCommand("project_reads.getProject", h.getProject),
		},
		{
			Name:  "get_project_guidance",
			order: 2,
			Description: "Returns the project's adopted ways of working as one text: the exact guidance, at the exact pinned versions, that the Forge's own agents are steered by and that the landing checks hold every Change to. " +
				"Strongly recommended before you write code on a project; the Forge does not push it into your context, so read it when you choose. get_project reports its size first so you can decide.",
			InputSchema: schemaObject(map[string]any{"slug": slugArg}, "slug"),
			command:     ownerCommand("project_reads.getProjectGuidance", h.getProjectGuidance),
		},
		{
			Name:        "get_application_contract",
			order:       3,
			Description: "Returns the project's committed application contract (.forge/app.yaml on its default branch) as text, plus the parsed form when it is valid.",
			InputSchema: schemaObject(map[string]any{"slug": slugArg}, "slug"),
			command:     ownerCommand("project_reads.getApplicationContract", h.getApplicationContract),
		},
	}
}
func (h projectReads) listProjects(c *Call, args map[string]any) (any, error) {
	all, err := h.scope.projects.Visible(c.Context)
	if err != nil {
		return nil, err
	}
	activity, err := h.work.ProjectActivity(c.Context, ids.Nil)
	if err != nil {
		return nil, err
	}
	projects := []map[string]any{}
	for i := range all {
		p := &all[i]
		pack, err := latestPack(c.Context, h.db, p.ID)
		if err != nil {
			return nil, err
		}
		row := map[string]any{
			"slug": p.Slug, "name": p.Name, "blurb": p.Blurb, "namespace": p.Namespace,
			"pack": pack, "defaultBranch": p.DefaultBranch,
			"openChanges": activity[p.ID].OpenChanges, "webUrl": c.projectURL(p.Slug),
		}
		if u := h.mainURL(c.Context, p); u != "" {
			row["mainUrl"] = u
		}
		projects = append(projects, row)
	}
	return map[string]any{"projects": projects}, nil
}
func (h projectReads) getProject(c *Call, args map[string]any) (any, error) {
	p, err := h.scope.projectForRead(c, argString(args, "slug"))
	if err != nil {
		return nil, err
	}
	activity, err := h.work.ProjectActivity(c.Context, p.ID)
	if err != nil {
		return nil, err
	}
	workflows, err := h.ways.Workflows(c.Context)
	if err != nil {
		return nil, err
	}
	pack, err := latestPack(c.Context, h.db, p.ID)
	if err != nil {
		return nil, err
	}
	deployments, err := h.deploymentsFor(c.Context, p)
	if err != nil {
		return nil, err
	}
	standing, err := h.latestStanding(c.Context, p)
	if err != nil {
		return nil, err
	}
	lanes, system := []map[string]any{}, []map[string]any{}
	for _, e := range workflows {
		l := map[string]any{"name": e.Name, "description": e.Doc.Metadata.Description,
			"tier": e.Doc.Metadata.EffectiveTier(), "version": e.Version}
		if e.Doc.Metadata.System {
			system = append(system, l)
		} else if selected, err := h.adoption.LaneSelected(c.Context, p.ID, e.Name); err != nil {
			return nil, err
		} else if selected {
			lanes = append(lanes, l)
		}
	}
	out := map[string]any{
		"slug": p.Slug, "name": p.Name, "blurb": p.Blurb, "namespace": p.Namespace, "icon": p.Icon,
		"pack": pack, "defaultBranch": p.DefaultBranch, "createdAt": p.CreatedAt,
		"hasRepository": p.DefaultBranch != "", "openChanges": activity[p.ID].OpenChanges,
		"lanes": lanes, "systemWorkflows": system,
		"deployments": deployments, "webUrl": c.projectURL(p.Slug),
	}
	for k, v := range guidanceOffer(c.Context, h.adoption, p.ID) {
		out[k] = v
	}
	if st := standing; st != nil {
		st["webUrl"] = c.BaseURL + st["webUrl"].(string)
		out["conformance"] = st
	}
	return out, nil
}
func (h projectReads) getProjectGuidance(c *Call, args map[string]any) (any, error) {
	p, err := h.scope.projectForRead(c, argString(args, "slug"))
	if err != nil {
		return nil, err
	}
	text, hash, imports, err := h.adoption.Compile(c.Context, p.ID)
	if err != nil {
		return nil, err
	}
	blocks := []map[string]any{}
	for _, im := range imports {
		if !im.Disabled {
			blocks = append(blocks, map[string]any{"name": im.Name, "version": im.Version, "bytes": im.Bytes})
		}
	}
	return map[string]any{
		"slug": p.Slug, "hash": hash, "bytes": len(text), "blocks": blocks, "text": text,
		"webUrl": c.projectURL(p.Slug) + "/settings/working",
	}, nil
}
func (h projectReads) getApplicationContract(c *Call, args map[string]any) (any, error) {
	p, err := h.scope.projectForRead(c, argString(args, "slug"))
	if err != nil {
		return nil, err
	}
	if p.DefaultBranch == "" {
		return nil, refuse("project %q is not repo-backed, so there is no contract to read", p.Slug)
	}
	repo := filepath.Join(h.dataDir, "workspaces", "project-"+p.Slug)
	raw, err := gitexec.Run(c.Context, repo, "show", p.DefaultBranch+":.forge/app.yaml")
	if err != nil {
		return nil, refuse("project %q has no .forge/app.yaml committed on %s", p.Slug, p.DefaultBranch)
	}
	out := map[string]any{"slug": p.Slug, "branch": p.DefaultBranch, "path": ".forge/app.yaml",
		"text": raw, "webUrl": c.projectURL(p.Slug)}
	if contract, perr := appcontract.Parse([]byte(raw)); perr == nil {
		out["parsed"] = contract
	} else {
		out["parseError"] = perr.Error()
	}
	return out, nil
}

func (h projectReads) mainURL(ctx context.Context, p *project.Project) string {
	e, err := h.deployments.Deployments.ByName(ctx, p.ID, "main")
	if err != nil || e.HostPort == 0 {
		return ""
	}
	return h.deployments.URL(ctx, p.Slug, e)
}
func (h projectReads) deploymentsFor(ctx context.Context, p *project.Project) ([]map[string]any, error) {
	out := []map[string]any{}
	all, err := h.deployments.Deployments.ForProject(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	for _, e := range all {
		url := h.deployments.URL(ctx, p.Slug, e)
		// "running" is absent, never false, when it could not be observed:
		// "observed" says why ("unavailable" or "unknown").
		entry := map[string]any{"name": e.Name, "url": url, "releaseRun": e.ReleaseRun, "updatedAt": e.UpdatedAt}
		if policy := e.UpdatePolicy(); policy != "" {
			entry["deploymentPolicy"] = policy
		}
		switch o := h.deployments.ObserveContainer(ctx, e); o.State {
		case deployment.ContainerUnavailable, deployment.ContainerUnknown:
			entry["observed"] = o.State.String()
		default:
			entry["running"] = o.State == deployment.ContainerRunning
		}
		out = append(out, entry)
	}
	return out, nil
}
func (h projectReads) latestStanding(ctx context.Context, p *project.Project) (map[string]any, error) {
	ps, err := h.conformance.LatestStanding(ctx, p.ID, p.Slug)
	if absent(err) || err == nil && ps == nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"standing": ps.Standing, "sentence": ps.Sentence, "reviewId": ps.ReviewID, "judgedAt": ps.JudgedAt,
		"webUrl": "/conformance/projects/" + p.Slug}, nil
}
