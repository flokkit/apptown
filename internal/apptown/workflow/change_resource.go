package workflow

import (
	"context"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/project"
)

// ChangeResource resolves current containing authority before adding change identity.
func ChangeResource(ctx context.Context, projects *project.Projects, ch *Change) (access.Resource, error) {
	res, err := projects.AuthorityResource(ctx, ch.ProjectID)
	if err != nil {
		return access.Resource{}, err
	}
	res.Type, res.ID, res.ChangeID = "change", ch.ID.String(), ch.ID
	return res, nil
}
