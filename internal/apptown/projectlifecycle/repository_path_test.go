package projectlifecycle

import (
	"path/filepath"

	"github.com/flokkit/apptown/internal/ids"
)

func hubDir(dataDir string, id ids.ID) string {
	return filepath.Join(dataDir, "workspaces", "project-"+id.String())
}
