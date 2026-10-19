package dataprotection

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/deploy/s3bucket"
	"flokkit.io/forge/internal/forge/dataprotection/internal/protectionsql"
	"flokkit.io/forge/internal/forge/project"
)

// Moving sets to the store (design/subsystems/deployment.md, "Backups and
// restore"):
// while a backup store is attached, every set is kept there. A set taken
// on the forge's disk before the store was attached is moved by the daily
// sweep, oldest first: each file is written to the store and held to the
// digest its record names, the manifest is written beside them, the row
// is changed in one guarded update, and only then is the folder removed.
// A set that does not match its record stays on the disk, saying why, and
// is pruned there in its turn.
//
// A forge that stops after the row changes and before the folder is
// removed leaves the folder: the row is the place of record and points at
// the checked objects, so nothing reads it again, and a person removes it
// (stock/docs/guides/running-apptown.md). The move logs its name first.

// MoveSetsToStore moves every set still on the forge's disk to the
// attached store; without a store there is nothing to move. It answers
// how many moved and, for each that stayed, why.
func (s *Service) MoveSetsToStore(ctx context.Context) (moved int, left []string, err error) {
	store, err := s.Store(ctx)
	if err != nil || store == nil {
		return 0, nil, err
	}
	rows, err := protectionsql.New(s.db.SQL()).SetsOnDisk(ctx, protectionsql.SetsOnDiskParams{Disk: StoreForgeDisk, OkState: BackupOK.String(), PartialState: BackupPartial.String()})
	if err != nil || len(rows) == 0 {
		return 0, nil, err
	}
	c, err := s.storeClientFor(ctx, store)
	if err != nil {
		return 0, nil, fmt.Errorf("the backup store: %w", err)
	}
	base, err := filepath.Abs(filepath.Join(s.dataDir, "backups"))
	if err != nil {
		return 0, nil, err
	}
	// A restore reading a set holds the shared lock: the move waits for
	// the next day rather than moving a set under it.
	files, err := LockBackupFiles(base, true)
	if err != nil {
		return 0, nil, err
	}
	defer files.Close()
	root, err := os.OpenRoot(base)
	if err != nil {
		return 0, nil, err
	}
	defer root.Close()
	for _, r := range rows {
		b, err := backupRecord(r, nil)
		if err != nil {
			return moved, left, err
		}
		// A project being deleted, or kept deleted, takes no new data work:
		// its sets stay where they are until it is restored or purged.
		if l, err := project.LifecycleIn(ctx, s.db, b.ProjectID); err != nil {
			return moved, left, err
		} else if l.State != project.Active {
			continue
		}
		why, err := s.moveSet(ctx, c, store, base, root, &b)
		if err != nil {
			return moved, left, err
		}
		if why != "" {
			left = append(left, fmt.Sprintf("set %d: %s", b.ID, why))
			note := "not moved to the backup store: " + why
			if !strings.Contains(b.Error, note) {
				// Said on the set though the move's context ended.
				if err := s.backups.Note(context.WithoutCancel(ctx), b.ID, note); err != nil {
					return moved, left, err
				}
			}
			continue
		}
		moved++
	}
	return moved, left, nil
}

// moveSet moves one set, or says why it stays on the disk. err is a
// failure to record, after which nothing more is moved.
func (s *Service) moveSet(ctx context.Context, c *s3bucket.Client, store *BackupStore, base string, root *os.Root, b *Backup) (string, error) {
	path, err := filepath.Abs(b.Path)
	if err != nil {
		return "its folder cannot be read: " + err.Error(), nil
	}
	rel, err := filepath.Rel(base, path)
	if err != nil || rel == "." || !filepath.IsLocal(rel) {
		return "its folder is not beneath the backup root", nil
	}
	prefix := filepath.ToSlash(rel)
	for _, it := range b.Items {
		if it.Error != "" || it.File == "" {
			continue
		}
		local := filepath.Join(path, it.File)
		if it.SHA256 == "" {
			return fmt.Sprintf("its copy %s records no digest to hold the move to", it.File), nil
		}
		if got := fileSHA256(local); got != it.SHA256 {
			return fmt.Sprintf("its copy %s on the disk does not match its record", it.File), nil
		}
		// PutPrivateFile reads back what the store holds: its size and
		// digest are the file's, which is the record's.
		if _, err := c.PutPrivateFile(ctx, store.Bucket, prefix+"/"+it.File, local); err != nil {
			return "writing " + it.File + " to the store: " + err.Error(), nil
		}
	}
	moved := *b
	moved.Path, moved.Store, moved.StoreEndpoint, moved.StoreBucket = prefix, StoreBackupStore, store.Endpoint, store.Bucket
	manifest, err := s.manifest(ctx, moved)
	if err == nil {
		err = c.PutPrivate(ctx, store.Bucket, prefix+"/manifest.json", "application/json", manifest)
	}
	if err != nil {
		return "writing its manifest to the store: " + err.Error(), nil
	}
	log.Printf("backup set %d: moving to the backup store at %s; its folder %s is removed next", b.ID, prefix, path)
	n, err := protectionsql.New(s.db.SQL()).MoveBackupToStore(ctx, protectionsql.MoveBackupToStoreParams{Store: StoreBackupStore, Prefix: prefix, Endpoint: store.Endpoint, Bucket: store.Bucket,
		ID: b.ID, Disk: StoreForgeDisk, Path: b.Path, OkState: BackupOK.String(), PartialState: BackupPartial.String()})
	if err != nil {
		return "", fmt.Errorf("set %d is in the store, but its move was not recorded: %w", b.ID, err)
	}
	if n != 1 {
		return "it changed while it moved", nil
	}
	if err := root.RemoveAll(rel); err != nil {
		log.Printf("backup set %d moved to the store; its old folder %s is left for a person to remove: %v", b.ID, path, err)
	}
	return "", nil
}

// ClearStaging removes what takes and restores left staged on the forge's
// disk more than a day ago: a forge that stopped mid-take leaves its
// staging folder. An operation's own staging is removed when it ends.
// A folder's age is its modification time, which the operating system
// stamped, so it is judged by the system clock, not the forge's.
func (s *Service) ClearStaging() (int, error) {
	now := clock.System()
	dir := filepath.Join(s.dataDir, "backups", ".staging")
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	cleared := 0
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "take-") && !strings.HasPrefix(e.Name(), "restore-") {
			continue
		}
		info, err := e.Info()
		if err != nil || now.Sub(info.ModTime()) < 24*time.Hour {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return cleared, err
		}
		cleared++
	}
	return cleared, nil
}
