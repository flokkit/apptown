package identity

import (
	"context"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/database"
)

// RemoveProjectIn removes a project's git pull token inside the caller's
// transaction. Grants scoped to the project are revoked history and stay.
func RemoveProjectIn(ctx context.Context, tx *database.Tx, projectID ids.ID) error {
	_, err := queries(tx).DeletePullToken(ctx, projectID)
	return err
}
