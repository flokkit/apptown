package dataprotection

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
)

func backupPruningFixture(t *testing.T) (*Backups, string, ids.ID, []Backup) {
	t.Helper()
	return backupPruningFixtureWithFirstPath(t, "")
}

func backupPruningFixtureWithFirstPath(t *testing.T, kind string) (*Backups, string, ids.ID, []Backup) {
	t.Helper()
	store := NewBackups(testDB(t))
	dir := t.TempDir()
	pid := createTestProject(t, store.db, "pruning", "Pruning")
	var backups []Backup
	for i := 0; i <= BackupKeep; i++ {
		path := filepath.Join(dir, "backups", "pruning", "main", fmt.Sprint(i))
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "data"), []byte("kept backup"), 0o600); err != nil {
			t.Fatal(err)
		}
		storedPath := path
		if i == 0 {
			switch kind {
			case "root":
				storedPath = filepath.Join(dir, "backups")
			case "traversal":
				storedPath = filepath.Join(dir, "backups") + string(os.PathSeparator) + ".." + string(os.PathSeparator) + "outside"
			case "symlink":
				storedPath = filepath.Join(dir, "backups", "link", "keep")
			}
		}
		id, err := store.Record(context.Background(), &Backup{ProjectID: pid, Deployment: "main", Path: storedPath, Status: BackupOK, TakenAt: "2026-09-15T00:00:00Z", Trigger: "manual", Actor: "fixture"}, "fixture")
		if err != nil {
			t.Fatal(err)
		}
		backups = append(backups, Backup{ID: id, Path: path})
	}
	return store, dir, pid, backups
}

func TestBackupPruningRejectsUnsafeOrUnremovablePaths(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"root", "traversal", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			store, dir, pid, backups := backupPruningFixtureWithFirstPath(t, kind)
			root := filepath.Join(dir, "backups")
			outside := filepath.Join(dir, "outside")
			if err := os.MkdirAll(filepath.Join(outside, "keep"), 0o700); err != nil {
				t.Fatal(err)
			}
			canary := filepath.Join(outside, "keep", "data")
			if err := os.WriteFile(canary, []byte("outside the backup root"), 0o600); err != nil {
				t.Fatal(err)
			}
			if kind == "symlink" {
				if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := store.Prune(context.Background(), root, pid, "main", BackupKeep); err == nil {
				t.Fatal("invalid or unremovable backup marked pruned")
			}
			if b, err := os.ReadFile(canary); err != nil || string(b) != "outside the backup root" {
				t.Fatalf("outside data changed: %q %v", b, err)
			}
			wantN(t, "pruned backups", backupsWhere(t, store.db, backupIn(BackupPruned)), 0)
			for _, b := range backups {
				if _, err := os.Stat(filepath.Join(b.Path, "data")); err != nil {
					t.Fatalf("retained backup lost: %v", err)
				}
			}
		})
	}
}

func TestBackupPruningReportsUnrecordedRemovalAndCanRetry(t *testing.T) {
	t.Parallel()
	store, dir, pid, backups := backupPruningFixture(t)
	root := filepath.Join(dir, "backups")
	mustExec(t, store.db, `CREATE FUNCTION veto_prune() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NULL; END $$`)
	mustExec(t, store.db, `CREATE TRIGGER veto_prune BEFORE UPDATE ON backups FOR EACH ROW EXECUTE FUNCTION veto_prune()`)
	if _, err := store.Prune(context.Background(), root, pid, "main", BackupKeep); err == nil || !strings.Contains(err.Error(), "files removed, but pruning was not recorded") {
		t.Fatalf("lost partial outcome: %v", err)
	}
	if _, err := os.Stat(backups[0].Path); !os.IsNotExist(err) {
		t.Fatalf("old files should have been removed: %v", err)
	}
	wantN(t, "pruned backups", backupsWhere(t, store.db, backupIn(BackupPruned)), 0)
	mustExec(t, store.db, `DROP TRIGGER veto_prune ON backups`)
	if _, err := store.Prune(context.Background(), root, pid, "main", BackupKeep); err != nil {
		t.Fatal(err)
	}
	wantN(t, "the oldest backup, pruned", backupsWhere(t, store.db, func(b ProjectBackup) bool { return b.ID == backups[0].ID && b.Status == BackupPruned }), 1)
	wantN(t, "kept backups", backupsWhere(t, store.db, backupIn(BackupOK)), BackupKeep)
	for _, b := range backups[1:] {
		if _, err := os.Stat(filepath.Join(b.Path, "data")); err != nil {
			t.Fatalf("newer backup removed: %v", err)
		}
	}
}

func TestBackupPruningReadFailureLeavesFilesAlone(t *testing.T) {
	t.Parallel()
	store, dir, pid, backups := backupPruningFixture(t)
	dbtest.FailNextStatement(t, store.db, "-- name: PrunableBackups :many")
	if _, err := store.Prune(context.Background(), filepath.Join(dir, "backups"), pid, "main", BackupKeep); err == nil {
		t.Fatal("ignored read failure")
	}
	for _, b := range backups {
		if _, err := os.Stat(filepath.Join(b.Path, "data")); err != nil {
			t.Fatal(err)
		}
	}
	wantN(t, "pruned backups", backupsWhere(t, store.db, backupIn(BackupPruned)), 0)
}

func TestBackupPruningDoesNotWaitForOrDeleteARestoresFiles(t *testing.T) {
	t.Parallel()
	store, dir, pid, backups := backupPruningFixture(t)
	root := filepath.Join(dir, "backups")
	files, err := LockBackupFiles(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer files.Close()
	if _, err := store.Prune(context.Background(), root, pid, "main", BackupKeep); err == nil || !strings.Contains(err.Error(), "busy") {
		t.Fatalf("pruning ignored restore lock: %v", err)
	}
	wantN(t, "pruned backups", backupsWhere(t, store.db, backupIn(BackupPruned)), 0)
	if _, err := os.Stat(backups[0].Path); err != nil {
		t.Fatalf("restore source lost: %v", err)
	}
	if err := files.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Prune(context.Background(), root, pid, "main", BackupKeep); err != nil {
		t.Fatal(err)
	}
	wantN(t, "pruned backups", backupsWhere(t, store.db, backupIn(BackupPruned)), 1)
}
