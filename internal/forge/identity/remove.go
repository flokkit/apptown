package identity

import (
	"context"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
)

// RemoveProjectIn removes a project's git pull token inside the caller's
// transaction. Grants scoped to the project are revoked history and stay.
func RemoveProjectIn(ctx context.Context, tx *database.Tx, projectID ids.ID) error {
	_, err := queries(tx).DeletePullToken(ctx, projectID)
	return err
}
