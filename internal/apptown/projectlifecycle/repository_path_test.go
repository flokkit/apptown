package projectlifecycle

import (
	"github.com/flokkit/apptown/internal/ids"
	"path/filepath"
)

func hubDir(dataDir string, id ids.ID) string {
	return filepath.Join(dataDir, "workspaces", "project-"+id.String())
}
