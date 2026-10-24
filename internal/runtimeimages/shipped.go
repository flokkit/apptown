package runtimeimages

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
// not), its list validated. It returns the list and its update.
func Shipped(dir, version string) (*List, *stockupdate.Update, error) {
	u, err := stockupdate.Shipped(dir, stockupdate.ToolVersions)
	if err != nil {
		return nil, nil, err
	}
	if _, err := u.Header.Judge(version); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", u.Path, err)
	}
	raw, err := fs.ReadFile(u.Content, stockupdate.ToolVersions.HeaderPath())
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", u.Path, err)
	}
	l, err := Parse(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", u.Path, err)
	}
	return l, u, nil
}

// LoadShipped reads the shipped tool versions in dir and Sets this
// process's runtime images from them.
func LoadShipped(dir, version string) (*List, error) {
	l, _, err := Shipped(dir, version)
	if err != nil {
		return nil, err
	}
	im, err := l.Images()
	if err != nil {
		return nil, err
	}
	Set(im)
	return l, nil
}
