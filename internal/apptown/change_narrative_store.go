package apptown

import (
	"context"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/database"
)

// narrativeReleaseNotes: what Work's narrative publication does to the
// release notes that quote the Change.
type narrativeReleaseNotes struct{}

func (narrativeReleaseNotes) FillIn(ctx context.Context, tx *database.Tx, projectID, changeID ids.ID, note string) error {
	return deployment.FillChangeReleaseNotesIn(ctx, tx, projectID, changeID, note)
}
