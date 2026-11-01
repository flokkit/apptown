package conformance

import (
	"context"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/database"
)

// RemoveProjectIn removes a project's dispositions inside the caller's
// transaction. Verdicts, fixes, adjudications and exceptions are the
// project's conformance history and follow the project row by key.
func RemoveProjectIn(ctx context.Context, tx *database.Tx, projectID ids.ID) error {
	return queries(tx).DeleteProjectDispositions(ctx, projectID)
}
