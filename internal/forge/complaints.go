package forge

import (
	"context"
	"fmt"
	"strings"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/adoption"
)

// sweepOldComplaints deletes complaints older than ninety days, whatever
// their state, and leaves one ledger line per sweep that removed anything.
// Runs from the scheduler.
func (a *App) sweepOldComplaints(ctx context.Context) { _, _ = a.Complaints.Expire(ctx) }

// complaintPackVersions names a project's adopted packs for a complaint
// about its way of working, from Adoption's memberships.
type complaintPackVersions struct{ db *database.DB }

func (c complaintPackVersions) PackVersionsOf(ctx context.Context, projectID ids.ID) (string, error) {
	packs, err := adoption.ProjectPacksIn(ctx, c.db, projectID)
	names := make([]string, 0, len(packs))
	for _, p := range packs {
		names = append(names, fmt.Sprintf("%s@%d", p.Pack, p.Version))
	}
	return strings.Join(names, ", "), err
}
