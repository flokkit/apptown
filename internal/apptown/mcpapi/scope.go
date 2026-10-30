package mcpapi

import (
	"context"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/workflow"
)

type Scope struct {
	projects  *project.Projects
	authority *identity.Service
}

func NewScope(projects *project.Projects, authority *identity.Service) Scope {
	return Scope{projects, authority}
}
func (s Scope) bySlug(ctx context.Context, slug string) (*project.Project, error) {
	p, err := s.projects.BySlug(ctx, slug)
	return &p, err
}
func (s Scope) mayRead(c *Call, p *project.Project) (bool, error) {
	res, err := s.projects.AuthorityResource(c.Context, p.ID)
	if err != nil {
		return false, err
	}
	dec, err := s.authority.Explain(c.Context, c.principal(), access.Permission{Capability: access.CapProjectRead, Resource: res})
	return dec.Allow, err
}

// readable returns the project with this id if the caller may read it. A
// missing project and one the caller may not see are refused alike; a failed
// read is an error, not "no such project".
func (s Scope) readable(c *Call, id ids.ID, refusal error) (*project.Project, error) {
	p, err := s.projects.ByID(c.Context, id)
	if absent(err) {
		return nil, refusal
	}
	if err != nil {
		return nil, err
	}
	if may, err := s.mayRead(c, &p); err != nil {
		return nil, err
	} else if !may {
		return nil, refusal
	}
	return &p, nil
}
func (s Scope) projectForRead(c *Call, slug string) (*project.Project, error) {
	if slug == "" {
		return nil, refuse("say which project: pass its slug (list_projects shows them)")
	}
	nope := refuse("there is no project %q you can see (list_projects shows the ones you can)", slug)
	p, err := s.bySlug(c.Context, slug)
	if absent(err) {
		return nil, nope
	}
	if err != nil {
		return nil, err
	}
	if may, err := s.mayRead(c, p); err != nil {
		return nil, err
	} else if !may {
		return nil, nope
	}
	return p, nil
}
func (s Scope) changeForRead(work *workflow.Service, c *Call, args map[string]any) (*workflow.Change, *project.Project, error) {
	id, err := argID(args, "id")
	if err != nil {
		return nil, nil, err
	}
	nope := refuse("there is no Change %s you can see (list_changes shows the ones you can)", id)
	ch, err := work.Change(c.Context, id)
	if absent(err) {
		return nil, nil, nope
	}
	if err != nil {
		return nil, nil, err
	}
	p, err := s.readable(c, ch.ProjectID, nope)
	if err != nil {
		return nil, nil, err
	}
	return ch, p, nil
}
func (s Scope) runForRead(work *workflow.Service, c *Call, args map[string]any) (*workflow.Run, *project.Project, error) {
	id, err := argID(args, "id")
	if err != nil {
		return nil, nil, err
	}
	nope := refuse("there is no run %d you can see (get_change names a Change's runs)", id)
	run, err := work.Run(c.Context, id)
	if absent(err) {
		return nil, nil, nope
	}
	if err != nil {
		return nil, nil, err
	}
	p, err := s.readable(c, run.ProjectID, nope)
	if err != nil {
		return nil, nil, err
	}
	return run, p, nil
}
func (s Scope) changeForWrite(work *workflow.Service, c *Call, args map[string]any) (*workflow.Change, *project.Project, error) {
	ch, p, err := s.changeForRead(work, c, args)
	if err != nil {
		return nil, nil, err
	}
	if ch.State == workflow.ChangeShipped || ch.State == workflow.ChangeAbandoned {
		return nil, nil, refuse("Change %d is %s; nothing more can be done to it", ch.ID, ch.State)
	}
	return ch, p, nil
}
func (s Scope) authorize(c *Call, tool string, cap access.Capability, res access.Resource) error {
	pr := c.principal()
	dec, err := s.authority.Decide(c.Context, pr, access.Permission{Capability: cap, Resource: res, Context: access.Context{Consequential: true, Route: "mcp:" + tool}})
	if err != nil {
		return err
	}
	if !dec.Allow {
		return refuse("%s", dec.Reason)
	}
	return nil
}
