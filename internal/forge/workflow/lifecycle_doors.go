package workflow

import (
	"context"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/project"
)

// refuseUnlessActive is Work's admission check against the project's
// lifecycle: change creation, start and send-back, run admission, a
// decision, a step's retry or claim, and every resume that locks a run's
// snapshot (landing, completion and its releases) refuse unless the
// project is active. Each door holds the project row first, so a deletion
// either sees its work or it refuses.
func refuseUnlessActive(ctx context.Context, tx *database.Tx, projectID ids.ID) error {
	return project.RefuseUnlessActiveIn(ctx, tx, projectID)
}
