package workflow

import (
	"context"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/project"
)

// ChangeResource preserves the legacy project lookup and containing scope.
func ChangeResource(ctx context.Context, projects *project.Projects, ch *Change) access.Resource {
	var p *project.Project
	record, err := projects.ByID(ctx, ch.ProjectID)
	if err == nil {
		p = &record
	}
	res := project.ObservedResource(ctx, projects, p)
	res.Type, res.ID, res.ChangeID = "change", ch.ID.String(), ch.ID
	return res
}
