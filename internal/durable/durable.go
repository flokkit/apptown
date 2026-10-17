// Package durable writes a file so that a crash leaves either the old file or
// the whole new one under its name, never a partial one.
package durable

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// Write puts body at dir/name with mode perm: written to a temporary file in
// dir, fsynced, moved into place and the directory fsynced. The move is a
// rename, replacing any file there, or with noReplace a hard link, which
// leaves an existing file untouched and reports existed. dir must exist.
func Write(dir, name string, body []byte, perm fs.FileMode, noReplace bool) (existed bool, err error) {
	tmp, err := os.CreateTemp(dir, "."+name+".*.tmp")
	if err != nil {
		return false, err
	}
	defer os.Remove(tmp.Name())
	if err := writeSynced(tmp, body, perm); err != nil {
		return false, err
	}
	final := filepath.Join(dir, name)
	if noReplace {
		err = os.Link(tmp.Name(), final)
		if errors.Is(err, fs.ErrExist) {
			return true, nil
		}
	} else {
		err = os.Rename(tmp.Name(), final)
	}
	if err != nil {
		return false, err
	}
	return false, syncDir(dir)
}

func writeSynced(f *os.File, body []byte, perm fs.FileMode) error {
	err := f.Chmod(perm)
	if err == nil {
		_, err = f.Write(body)
	}
	if err == nil {
		err = f.Sync()
	}
	return errors.Join(err, f.Close())
}

// SyncDir makes the names in dir durable: a caller that accepts a file it
// did not write itself (one an earlier Write left, perhaps before its own
// directory sync failed) syncs its directory before relying on it.
func SyncDir(dir string) error { return syncDir(dir) }

// syncDir is a variable so a test can fail the directory fsync.
var syncDir = func(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
