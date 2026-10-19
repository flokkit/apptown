package scm

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"flokkit.io/forge/internal/fault"
)

// A project's working tree can hold whatever its history holds, including
// symbolic links: a project started from existing code brings someone
// else's tree. When the forge itself writes a file into a tree (the ignore
// file at setup, a pack's starter files, a toolchain declaration), it must
// never follow such a link out of the tree: a committed ".gitignore" that
// links to a path on the forge's host would otherwise have the forge
// create that file. These helpers refuse a link anywhere on the way and
// never open the last name through one.

// UnsafePath refuses a write whose path, inside the tree, is or passes
// through a symbolic link or something that is not a directory.
type UnsafePath struct{ Path, Why string }

func (e *UnsafePath) Error() string {
	return fmt.Sprintf("AppTown will not write %s: %s", e.Path, e.Why)
}
func (e *UnsafePath) FaultKind() fault.Kind { return fault.Unprocessable }

// parentsIn makes rel's parent directories inside root, refusing a link
// or a non-directory on the way, and returns the file's full path.
func parentsIn(root, rel string) (string, error) { return walkParents(root, rel, true) }

// walkParents checks rel's parents inside root as parentsIn does, making a
// missing one only when create is set; a missing one otherwise is
// os.ErrNotExist.
func walkParents(root, rel string, create bool) (string, error) {
	rel = filepath.Clean(rel)
	if !filepath.IsLocal(rel) {
		return "", &UnsafePath{Path: rel, Why: "it is not inside the repository"}
	}
	dir := root
	parts := strings.Split(rel, string(filepath.Separator))
	for _, part := range parts[:len(parts)-1] {
		dir = filepath.Join(dir, part)
		info, err := os.Lstat(dir)
		switch {
		case errors.Is(err, os.ErrNotExist) && !create:
			return "", err
		case errors.Is(err, os.ErrNotExist):
			if err := os.Mkdir(dir, 0o755); err != nil {
				return "", err
			}
		case err != nil:
			return "", err
		case info.Mode()&os.ModeSymlink != 0:
			return "", &UnsafePath{Path: rel, Why: filepath.Join(parts[:len(parts)-1]...) + " is a symbolic link in the repository"}
		case !info.IsDir():
			return "", &UnsafePath{Path: rel, Why: "a parent of it is not a directory"}
		}
	}
	return filepath.Join(root, rel), nil
}

// CreateInTree writes a new file at rel inside root unless something is
// already there under that name: a file, a directory, or a link (which is
// left alone, not followed). It reports whether it wrote.
func CreateInTree(root, rel string, body []byte, mode os.FileMode) (bool, error) {
	path, err := parentsIn(root, rel)
	if err != nil {
		return false, err
	}
	if _, err := os.Lstat(path); err == nil {
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	// O_EXCL fails on any existing name, a link included, so a link made
	// after the check is not followed either.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return false, err
	}
	if _, err := f.Write(body); err != nil {
		_ = f.Close()
		return false, err
	}
	return true, f.Close()
}

// UpdateInTree rewrites the regular file at rel inside root with
// change(its contents), refusing a link. A missing file is not an error:
// it reports false. It reports whether the contents changed.
func UpdateInTree(root, rel string, change func([]byte) []byte) (bool, error) {
	path, err := parentsIn(root, rel)
	if err != nil {
		return false, err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, &UnsafePath{Path: rel, Why: "it is not a regular file"}
	}
	f, err := os.OpenFile(path, os.O_RDWR|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return false, err
	}
	defer f.Close()
	old, err := io.ReadAll(f)
	if err != nil {
		return false, err
	}
	updated := change(old)
	if string(updated) == string(old) {
		return false, nil
	}
	if err := f.Truncate(0); err != nil {
		return false, err
	}
	if _, err := f.WriteAt(updated, 0); err != nil {
		return false, err
	}
	return true, f.Close()
}

// ReadInTree reads the regular file at rel inside root without following a
// link on the way or at the end; a missing file is os.ErrNotExist.
func ReadInTree(root, rel string) ([]byte, error) {
	path, err := walkParents(root, rel, false)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, &UnsafePath{Path: rel, Why: "it is not a regular file"}
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

// WriteInTree makes the file at rel inside root hold body: a new file, or
// a regular file rewritten; a link is refused, never followed.
func WriteInTree(root, rel string, body []byte, mode os.FileMode) error {
	created, err := CreateInTree(root, rel, body, mode)
	if err != nil || created {
		return err
	}
	_, err = UpdateInTree(root, rel, func([]byte) []byte { return body })
	return err
}
