package adoption

import (
	"context"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/database"
)

// PublishedWorkflows reads the current library declarations in one read.
func (*Service) PublishedWorkflows(ctx context.Context, q database.Queryer) ([]*ResolvedWorkflow, error) {
	catalog, err := wow.ReadWorkflowCatalogIn(ctx, q)
	if err != nil {
		return nil, err
	}
	var out []*ResolvedWorkflow
	for _, entry := range catalog.Workflows {
		doc, err := schema.ParseWorkflowStored([]byte(entry.Yaml))
		if err != nil {
			return nil, err
		}
		out = append(out, &ResolvedWorkflow{Name: entry.Name, Version: int(entry.LatestVersion), Doc: doc})
	}
	return out, nil
}
