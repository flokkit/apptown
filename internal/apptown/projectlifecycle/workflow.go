package projectlifecycle

import (
	"context"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
)

// ResolveWorkflow resolves an available workflow and checks current project policies.
func (s *Service) ResolveWorkflow(ctx context.Context, p *project.Project, requested string) (*adoption.ResolvedWorkflow, error) {
	revision, err := adoption.CurrentReceiptIn(ctx, s.db, p.ID)
	if _, err := database.Lookup(err); err != nil {
		return nil, err
	}
	return s.adoption.Workflow(ctx, adoption.WorkflowRequest{Source: adoption.Source{ProjectID: p.ID, Revision: revision}, Name: requested, ProjectSlug: p.Slug})
}
