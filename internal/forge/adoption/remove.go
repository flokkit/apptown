package adoption

import (
	"context"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/adoption/internal/adoptionsql"
)

// RemoveProjectIn removes the Ways of Working rows a project carries,
// inside the caller's transaction: its policies, guidance imports, lanes,
// extensions, pack memberships and adoption history.
func RemoveProjectIn(ctx context.Context, tx *database.Tx, projectID ids.ID) error {
	q := adoptionsql.New(tx.SQL())
	for _, remove := range []func(context.Context, ids.ID) (int64, error){q.DeletePoliciesForProject, q.DeleteGuidanceImportsForProject, q.DeleteProjectLanes, q.DeleteProjectExtensions, q.DeleteProjectPacks, q.DeleteProjectAdoptions, q.DeleteCurrentAdoption} {
		if _, err := remove(ctx, projectID); err != nil {
			return err
		}
	}
	return nil
}
