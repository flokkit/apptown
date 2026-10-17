package httpapi

import (
	"context"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/adoption"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/wow"
)

type libraryCatalog struct {
	workflows []wow.WorkflowListing
	selected  map[string][]string
	projects  []string
}

// The catalog uses three bulk reads regardless of the number of workflows.
func loadLibraryCatalog(ctx context.Context, db database.Queryer) (libraryCatalog, error) {
	var c libraryCatalog
	records, err := wow.ReadWorkflowCatalogIn(ctx, db)
	if err != nil {
		return c, err
	}
	c.workflows = records.Workflows
	c.selected, err = adoption.WorkflowSelectionsIn(ctx, db)
	if err != nil {
		return c, err
	}
	c.projects, err = project.SlugsIn(ctx, db)
	return c, err
}
