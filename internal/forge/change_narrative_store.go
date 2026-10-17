package forge

import (
	"context"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/deployment"
)

// narrativeReleaseNotes: what Work's narrative publication does to the
// release notes that quote the Change.
type narrativeReleaseNotes struct{}

func (narrativeReleaseNotes) FillIn(ctx context.Context, tx *database.Tx, projectID, changeID ids.ID, note string) error {
	return deployment.FillChangeReleaseNotesIn(ctx, tx, projectID, changeID, note)
}
