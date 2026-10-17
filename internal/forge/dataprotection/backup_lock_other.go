//go:build !unix

package dataprotection

import (
	"errors"
	"io"
)

func LockBackupFiles(string, bool) (io.Closer, error) {
	return nil, errors.New("backup restore and pruning require filesystem locking on a Unix host")
}
