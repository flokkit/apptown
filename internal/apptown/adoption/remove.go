package adoption

import (
	"context"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/adoption/internal/adoptionsql"
	"github.com/flokkit/apptown/internal/database"
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
