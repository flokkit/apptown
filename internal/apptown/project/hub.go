package project

import "path/filepath"

// HubPath is where a project's repository lives under the data directory.
func HubPath(dataDir, slug string) string {
	return filepath.Join(dataDir, "workspaces", "project-"+slug)
}
