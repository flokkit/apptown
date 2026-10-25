package projectlifecycle

import "path/filepath"

func hubDir(dataDir, slug string) string {
	return filepath.Join(dataDir, "workspaces", "project-"+slug)
}
