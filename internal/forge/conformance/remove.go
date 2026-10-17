package conformance

import (
	"context"
	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
)

// RemoveProjectIn removes a project's dispositions inside the caller's
// transaction. Verdicts, fixes, adjudications and exceptions are the
// project's conformance history and follow the project row by key.
func RemoveProjectIn(ctx context.Context, tx *database.Tx, projectID ids.ID) error {
	return queries(tx).DeleteProjectDispositions(ctx, projectID)
}
