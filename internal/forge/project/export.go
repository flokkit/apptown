package project

import (
	"context"
	"encoding/json"
	"flokkit.io/forge/internal/ids"
	"path/filepath"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/project/internal/projectsql"
)

// ExportIn reads a project's own row whole, for moving it to another AppTown.
// The receiving AppTown keeps its settings, not its id, group or approvers.
func ExportIn(ctx context.Context, q database.Queryer, projectID ids.ID) (json.RawMessage, error) {
	row, err := projectsql.New(q.SQL()).ExportProjectRow(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(row), nil
}

// HubPath is where a project's repository lives under the data directory.
func HubPath(dataDir, slug string) string {
	return filepath.Join(dataDir, "workspaces", "project-"+slug)
}
