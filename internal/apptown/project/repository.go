package project

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/flokkit/apptown/internal/ids"
)

// RepositoryRef identifies a project's hub from an owner record or projection.
// It is not an authorization decision: the caller must have established that
// ID and Slug belong together before constructing it.
type RepositoryRef struct {
	ID   ids.ID
	Slug string
}

// Path is the current on-disk hub location. It neither probes nor creates it.
// Change and run workspaces have their own identities and layouts.
func (r RepositoryRef) Path(dataDir string) (string, error) {
	if r.ID.IsZero() || !ValidSlug(r.Slug) {
		return "", fmt.Errorf("project repository needs an identified project with a valid address")
	}
	return filepath.Join(dataDir, "workspaces", "project-"+r.Slug), nil
}

// Repository resolves the hub belonging to this project record.
func (p Project) Repository(dataDir string) (string, error) {
	return (RepositoryRef{ID: p.ID, Slug: p.Slug}).Path(dataDir)
}

// RepositoriesOnDisk lists observed hub directories for boot maintenance.
// It does not establish project ownership: ordinary project operations use
// RepositoryRef. Unowned directories are not adopted or removed by this read.
func RepositoriesOnDisk(dataDir string) ([]string, error) {
	root := filepath.Join(dataDir, "workspaces")
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "project-") {
			paths = append(paths, filepath.Join(root, entry.Name()))
		}
	}
	return paths, nil
}
