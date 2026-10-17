package secret

import (
	"context"
	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
)

// RemoveProjectIn removes a project's secret records inside the caller's
// transaction: the built-in store's values its bindings point at (and no
// other project's), then the bindings, then the chosen profiles.
func RemoveProjectIn(ctx context.Context, tx *database.Tx, projectID ids.ID) error {
	q := queries(tx)
	if err := q.DeleteProjectSecretValues(ctx, projectID); err != nil {
		return err
	}
	if err := q.DeleteProjectBindings(ctx, projectID); err != nil {
		return err
	}
	return q.DeleteProjectProfiles(ctx, projectID)
}
