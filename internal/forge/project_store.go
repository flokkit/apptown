package forge

import (
	"context"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/forge/project"
)

// Projects is the project record's supported API; AppTown keeps the doors
// and the acts that span domains.

func (a *App) projectByID(ctx context.Context, id ids.ID) *project.Project {
	p, err := project.ByIDIn(ctx, a.DB, id)
	if err != nil {
		return nil
	}
	return &p
}
