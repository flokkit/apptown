//go:build unix

package dataprotection

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestBackupLockReleaseDoesNotDependOnInheritedDescriptorLifetime(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "backups")
	held, err := lockBackupFiles(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	// fork and dup share the open-file description. Retain that duplicate
	// deterministically instead of relying on process-launch timing in this test.
	fd, err := syscall.Dup(int(held.(*backupFileLock).file.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	syscall.CloseOnExec(fd)
	inherited := os.NewFile(uintptr(fd), "inherited-backup-lock")
	defer inherited.Close()
	if other, err := lockBackupFiles(root, true); err == nil {
		other.Close()
		t.Fatal("restore did not exclude pruning")
	}
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	pruning, err := lockBackupFiles(root, true)
	if err != nil {
		t.Fatalf("finished restore's duplicate retained its lock: %v", err)
	}
	defer pruning.Close()
	// A deferred second Close must not unlock a new owner's reused descriptor.
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	if other, err := lockBackupFiles(root, true); err == nil {
		other.Close()
		t.Fatal("old owner released a new owner's lock")
	}
}

func TestBackupLockReleasePreservesOtherRestores(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "backups")
	first, err := lockBackupFiles(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := lockBackupFiles(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if pruning, err := lockBackupFiles(root, true); err == nil {
		pruning.Close()
		t.Fatal("first restore released the second restore's lock")
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	pruning, err := lockBackupFiles(root, true)
	if err != nil {
		t.Fatal(err)
	}
	defer pruning.Close()
}
