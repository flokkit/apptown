package forge

import (
	"context"

	"flokkit.io/forge/internal/ids"
)

func (a *App) startAcceptedLanding(ctx context.Context, id ids.ID) error {
	return workflowFollowUps(a).Landings.Advance(ctx, id)
}
