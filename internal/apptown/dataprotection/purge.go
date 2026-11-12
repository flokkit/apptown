package dataprotection

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/dataprotection/internal/protectionsql"
	"github.com/flokkit/apptown/internal/apptown/operations"
)

// A project's backup sets belong to the project: they are kept while it is
// kept deleted, and go when it is purged, on disk and in the store
// (design/subsystems/project-deletion.md).

// TakeDownForDeletion admits a take-down under the deletion's scoped binding.
func (s *Service) TakeDownForDeletion(ctx context.Context, projectID ids.ID, name string, binding access.Principal) (*operations.Operation, error) {
	return s.TakeDown(ctx, RemoveDeployment{ProjectID: projectID, Name: name, withProject: true, binding: &binding})
}

// PurgeProjectBackups removes every backup set of a project being purged:
// each set's files on AppTown's disk or its copies in the store, then
// its record. allowed is asked before each set. A set that could not be
// removed is named and keeps its record, so the next purge tries it again.
func (s *Service) PurgeProjectBackups(ctx context.Context, projectID ids.ID, allowed func(context.Context) error) (removed int, left []string, err error) {
	q := protectionsql.New(s.db.SQL())
	rows, err := q.ProjectBackupsToPurge(ctx, protectionsql.ProjectBackupsToPurgeParams{ProjectID: projectID, PrunedState: backupPruned.String()})
	if err != nil {
		return 0, nil, err
	}
	removed, left, err = s.removeSets(ctx, rows, allowed)
	if err != nil {
		return removed, left, err
	}
	if _, err := q.RemovePurgedBackups(ctx, protectionsql.RemovePurgedBackupsParams{ProjectID: projectID, PrunedState: backupPruned.String()}); err != nil {
		return removed, left, fmt.Errorf("the sets were removed, but their records were not: %w", err)
	}
	return removed, left, nil
}

// PurgeOrphanBackups removes the sets of projects deleted before projects
// were kept (their records already gone), 30 days after each deletion.
func (s *Service) PurgeOrphanBackups(ctx context.Context, now time.Time) (removed int, left []string, err error) {
	q := protectionsql.New(s.db.SQL())
	rows, err := q.OrphanBackupsDue(ctx, protectionsql.OrphanBackupsDueParams{PrunedState: backupPruned.String(), Now: now})
	if err != nil {
		return 0, nil, err
	}
	removed, left, err = s.removeSets(ctx, rows, nil)
	if err != nil {
		return removed, left, err
	}
	if _, err := q.RemoveOrphanBackupRecords(ctx, backupPruned.String()); err != nil {
		return removed, left, fmt.Errorf("the sets were removed, but their records were not: %w", err)
	}
	return removed, left, nil
}

// removeSets removes each set's files or store copies and marks it pruned;
// what fails is named and the next set tried.
func (s *Service) removeSets(ctx context.Context, rows []protectionsql.Backup, allowed func(context.Context) error) (removed int, left []string, err error) {
	dir := DiskDirectory(s.dataDir)
	// An AppTown that never wrote a set to its disk has no backup root; its
	// sets' files are already absent, and pruning records them so.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return 0, nil, err
	}
	for _, r := range rows {
		if allowed != nil {
			if err := allowed(ctx); err != nil {
				return removed, left, err
			}
		}
		var rerr error
		if r.Store == StoreBackupStore {
			rerr = s.pruneStoreSets(ctx, []protectionsql.Backup{r}, true)
		} else {
			rerr = s.backups.pruneSets(ctx, dir, []protectionsql.Backup{r}, true)
		}
		if rerr != nil {
			left = append(left, fmt.Sprintf("set %d of %s: %v", r.ID, r.Deployment, rerr))
			continue
		}
		removed++
	}
	return removed, left, nil
}
