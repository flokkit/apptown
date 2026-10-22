package projectlifecycle

import (
	"context"
	"log"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
)

// SelectedWorkflow resolves the version of a workflow the project's adoption
// revision pins, gated by the project's selection unless the workflow is a
// system one.
func (s *Service) SelectedWorkflow(ctx context.Context, p *project.Project, requested string) (*adoption.ResolvedWorkflow, error) {
	revision, err := adoption.CurrentReceiptIn(ctx, s.db, p.ID)
	if _, err := database.Lookup(err); err != nil {
		log.Printf("read failed: %v", err)
	}
	return s.adoption.Workflow(ctx, adoption.WorkflowRequest{Source: adoption.Source{ProjectID: p.ID, Revision: revision}, Name: requested, ProjectSlug: p.Slug})
}
