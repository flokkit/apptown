package toolversions

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/flokkit/apptown/internal/stockupdate"
)

// ShippedDir is where a process finds the updates its release shipped:
// dir when given (-shipped), else stockupdate.Dir of its own executable.
func ShippedDir(dir string) (string, error) {
	if dir != "" {
		return dir, nil
	}
	exe, err := os.Executable()
	if err == nil {
		// A link to the binary (/usr/local/bin/apptownd) is not where its
		// release unpacked it.
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		return "", fmt.Errorf("finding the shipped updates beside this binary: %w", err)
	}
	return stockupdate.Dir(exe), nil
}

// Shipped reads the tool versions update in dir that this build, of
// version, can read: the newest, its worksWith judged (a dev- build's is
// not), its list validated, as a snapshot named by the update.
func Shipped(dir, version string) (Snapshot, error) {
	u, err := stockupdate.Shipped(dir, stockupdate.ToolVersions)
	if err != nil {
		return Snapshot{}, err
	}
	if _, err := u.Header.Judge(version); err != nil {
		return Snapshot{}, fmt.Errorf("%s: %w", u.Path, err)
	}
	raw, err := fs.ReadFile(u.Content, stockupdate.ToolVersions.HeaderPath())
	if err != nil {
		return Snapshot{}, fmt.Errorf("%s: %w", u.Path, err)
	}
	s, err := NewSnapshot(u.Header.Name, raw)
	if err != nil {
		return Snapshot{}, fmt.Errorf("%s: %w", u.Path, err)
	}
	return s, nil
}
