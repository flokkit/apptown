//go:build unix

package dataprotection

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

// LockBackupFiles holds the files for a reader against pruning and moving.
func LockBackupFiles(directory string) (io.Closer, error) { return lockBackupFiles(directory, false) }

// Restores hold a shared lock on backup files; pruning requires exclusive
// access. Acquisition never waits. Close explicitly unlocks before releasing
// the descriptor; a duplicate inherited during process launch must not extend
// the lease. Process exit also releases it without durable recovery. Keep the
// lock file outside the prunable tree and never remove it.
func lockBackupFiles(directory string, exclusive bool) (io.Closer, error) {
	file, err := os.OpenFile(filepath.Clean(directory)+".retention.lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	mode := syscall.LOCK_SH
	if exclusive {
		mode = syscall.LOCK_EX
	}
	if err := syscall.Flock(int(file.Fd()), mode|syscall.LOCK_NB); err != nil {
		file.Close()
		return nil, fmt.Errorf("backup files are busy or could not be locked; retry later: %w", err)
	}
	return &backupFileLock{file: file}, nil
}

// A lock's release is independent of duplicated descriptors and idempotent even
// if the operating system reuses the original descriptor for a later lock.
type backupFileLock struct {
	file *os.File
	once sync.Once
	err  error
}

func (l *backupFileLock) Close() error {
	l.once.Do(func() {
		unlocked := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
		l.err = errors.Join(unlocked, l.file.Close())
	})
	return l.err
}
