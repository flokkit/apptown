package adoption

import (
	"context"
	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/forge/wow"
)

// AvailableWorkflows lists current catalog declarations selected by the project,
// including system workflows. This is a presentation inventory; Workflow resolves
// the pinned declaration and authority used for admission.
func (s *Service) AvailableWorkflows(ctx context.Context, projectID ids.ID) ([]wow.WorkflowEntry, error) {
	if s.deps.Ways == nil {
		return nil, ErrCatalogUnavailable
	}
	entries, err := s.deps.Ways.Workflows(ctx)
	if err != nil {
		return nil, err
	}
	var out []wow.WorkflowEntry
	for _, e := range entries {
		selected := e.Doc.Metadata.System
		if !selected {
			if selected, err = s.LaneSelected(ctx, projectID, e.Name); err != nil {
				return nil, err
			}
		}
		if selected {
			out = append(out, e)
		}
	}
	return out, nil
}
