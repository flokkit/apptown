package dataprotection

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/dataprotection/internal/protectionsql"
)

// PruneSets removes a deployment's ordinary sets past keep, counted across
// both places a set can be (AppTown's disk and the backup store), each
// deleted where it is: files on the disk (Backups.Prune), objects in the
// store (pruneStore).
func (s *Service) PruneSets(ctx context.Context, projectID ids.ID, deployment string, keep int) error {
	inStore, err := s.backups.Prune(ctx, DiskDirectory(s.dataDir), projectID, deployment, keep)
	return errors.Join(err, s.pruneStore(ctx, inStore))
}

// Prune selects a deployment's ordinary sets past keep, counted across
// both places, and removes those on AppTown's disk: files first, then
// the record, under the exclusive lock restores share. Filesystem removal
// cannot share a transaction with its database receipt, so removal comes
// first and success is recorded after; a missing directory is safe to
// retry after a failed write. Errors identify which side completed instead
// of claiming that retained files were pruned or that deleted files are
// still present. The selected sets in the store are returned for the
// service, which holds the store's client, whatever happened on the disk.
func (s *Backups) Prune(ctx context.Context, directory string, projectID ids.ID, deployment string, keep int) (inStore []protectionsql.Backup, err error) {
	old, err := protectionsql.New(s.db.SQL()).PrunableBackups(ctx, protectionsql.PrunableBackupsParams{
		ProjectID:    projectID,
		Deployment:   deployment,
		OkState:      BackupOK.String(),
		PartialState: BackupPartial.String(),
		KeepRows:     int64(keep),
		FinalTrigger: TriggerTakeDown,
	})
	if err != nil {
		return nil, err
	}
	var disk []protectionsql.Backup
	for _, r := range old {
		if r.Store == StoreBackupStore {
			inStore = append(inStore, r)
		} else {
			disk = append(disk, r)
		}
	}
	return inStore, s.prune(ctx, directory, disk)
}

func (s *Backups) prune(ctx context.Context, directory string, old []protectionsql.Backup) error {
	return s.pruneSets(ctx, directory, old, false)
}

// pruneSets is prune; purging settles a set whatever its state (a failed
// or partial set goes with its project too), and one with no files on the
// disk is only settled.
func (s *Backups) pruneSets(ctx context.Context, directory string, old []protectionsql.Backup, purging bool) error {
	if len(old) == 0 {
		return nil
	}
	base, err := filepath.Abs(directory)
	if err != nil {
		return err
	}
	files, err := lockBackupFiles(base, true)
	if err != nil {
		return err
	}
	defer files.Close()
	root, err := os.OpenRoot(base)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, b := range old {
		settleFrom := BackupOK.String()
		if purging {
			settleFrom = b.Status
		}
		if purging && b.Path == "" {
			if err := s.settlePruned(ctx, b, settleFrom); err != nil {
				return err
			}
			continue
		}
		path, err := filepath.Abs(b.Path)
		if err != nil {
			return fmt.Errorf("backup %d path: %w", b.ID, err)
		}
		rel, err := filepath.Rel(base, path)
		if err != nil || rel == "." || !filepath.IsLocal(rel) {
			return fmt.Errorf("backup %d is not a directory beneath the backup root", b.ID)
		}
		if err := root.RemoveAll(rel); err != nil {
			return fmt.Errorf("removing backup %d files: %w", b.ID, err)
		}
		if err := s.settlePruned(ctx, b, settleFrom); err != nil {
			return err
		}
	}
	return nil
}

// settlePruned records a set whose files are gone as pruned: from ok or
// partial, or from also, its own state when a purge settles it.
// Its files are already gone: the record is written though ctx has ended.
func (s *Backups) settlePruned(ctx context.Context, b protectionsql.Backup, also string) error {
	if _, err := protectionsql.New(s.db.SQL()).PruneBackup(context.WithoutCancel(ctx), protectionsql.PruneBackupParams{
		Status:       BackupPruned.String(),
		ID:           b.ID,
		Path:         b.Path,
		OkState:      also,
		PartialState: BackupPartial.String(),
		PrunedState:  BackupPruned.String(),
	}); err != nil {
		return fmt.Errorf("backup %d files removed, but pruning was not recorded: %w", b.ID, err)
	}
	return nil
}
