package deployment

import (
	"context"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/database"
)

// RemoveProjectIn removes Delivery's rows for a project inside the
// caller's transaction: releases with their events and change links,
// bundles, and deployments with what hangs off them (attempts, need
// instances, reconciliations follow by key). Retained resources are the
// record of what was kept and survive.
func RemoveProjectIn(ctx context.Context, tx *database.Tx, projectID ids.ID) error {
	q := deploymentsql.New(tx.SQL())
	for _, remove := range []func(context.Context, ids.ID) (int64, error){q.RemoveProjectReleaseEvents, q.RemoveProjectReleaseChanges, q.RemoveProjectReleases, q.RemoveProjectReleaseBundles, q.RemoveProjectDeployments} {
		if _, err := remove(ctx, projectID); err != nil {
			return err
		}
	}
	return nil
}
