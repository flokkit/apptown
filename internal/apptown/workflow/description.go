package workflow

import (
	"context"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/ids"
)

// LandedTitles reads the last ten observed landings, newest first.
func (s *Service) LandedTitles(ctx context.Context, id ids.ID) ([]string, error) {
	return workflowsql.New(s.db.SQL()).LastLandedTitles(ctx, id)
}
