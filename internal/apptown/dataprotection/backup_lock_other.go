//go:build !unix

package dataprotection

import (
	"errors"
	"io"
)

// LockBackupFiles holds the files for a reader against pruning and moving.
func LockBackupFiles(directory string) (io.Closer, error) { return lockBackupFiles(directory, false) }

func lockBackupFiles(string, bool) (io.Closer, error) {
	return nil, errors.New("backup restore and pruning require filesystem locking on a Unix host")
}
