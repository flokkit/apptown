package httpapi

import (
	"context"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/database"
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
