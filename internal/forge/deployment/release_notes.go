package deployment

import (
	"context"
	"fmt"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/deployment/internal/releasesql"
)

// Delivery owns which linked releases can inherit a Change's prose. The caller
// holds the project lock and commits this with the Change publication/receipt.
// Existing nonempty notes are preserved, including notes written by settlement.
func FillChangeReleaseNotesIn(ctx context.Context, tx *database.Tx, projectID, changeID ids.ID, note string) error {
	counts, err := releasesql.New(tx.SQL()).FillChangeReleaseNotes(ctx, releasesql.FillChangeReleaseNotesParams{ProjectID: projectID, ChangeID: changeID, Note: note})
	if err != nil {
		return err
	}
	if counts.SelectedCount != counts.UpdatedCount {
		return fmt.Errorf("linked release notes were not completely recorded")
	}
	return nil
}
