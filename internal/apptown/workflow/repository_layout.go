package workflow

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/flokkit/apptown/internal/apptown/project/repositorylayout"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
)

// LayoutWorkspaceReferences inventories change/run and snapshot roots by their
// durable owner. They keep their UUID names when the installation root changes.
func LayoutWorkspaceReferences(ctx context.Context, q database.Queryer, oldRoot, newRoot string) ([]repositorylayout.Reference, error) {
	rows, err := workflowsql.New(q.SQL()).LayoutWorkspaceOwners(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]repositorylayout.Reference, 0, 2*len(rows))
	for _, row := range rows {
		if row.ID.IsZero() || row.ProjectID.IsZero() || (row.Kind != "change" && row.Kind != "run") {
			return nil, fmt.Errorf("invalid workspace owner in layout inventory")
		}
		for _, prefix := range []string{row.Kind, "asof-" + row.Kind} {
			name := prefix + "-" + row.ID.String()
			out = append(out, repositorylayout.Reference{Owner: row.ProjectID, Before: filepath.Join(oldRoot, "workspaces", name), After: filepath.Join(newRoot, "workspaces", name)})
		}
	}
	return out, nil
}
