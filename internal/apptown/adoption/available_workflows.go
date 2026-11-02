package adoption

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/flokkit/apptown/internal/database"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/wow"
)

// AvailableWorkflows lists core and extra library workflows and pack workflows,
// including system workflows. This is a presentation inventory; Workflow resolves
// the procedure version and current policies used for admission.
func (s *Service) AvailableWorkflows(ctx context.Context, projectID ids.ID) ([]wow.WorkflowEntry, error) {
	if s.deps.Ways == nil {
		return nil, errCatalogUnavailable
	}
	entries, err := s.deps.Ways.Workflows(ctx)
	if err != nil {
		return nil, err
	}
	packWorkflows, err := packWorkflowNamesIn(ctx, s.db, projectID)
	if err != nil {
		return nil, err
	}
	var out []wow.WorkflowEntry
	for _, e := range entries {
		if e.Doc.Metadata.System || !e.Withdrawn || packWorkflows[e.Name] {
			out = append(out, e)
		}
	}
	return out, nil
}

// ScheduledWorkflowNames returns only explicit schedules declarations from the
// project's adopted pack versions. Availability alone never schedules a workflow.
func (s *Service) ScheduledWorkflowNames(ctx context.Context, projectID ids.ID) (map[string]bool, error) {
	memberships, err := ProjectPacksIn(ctx, s.db, projectID)
	if err != nil {
		return nil, err
	}
	names := map[string]bool{}
	for _, m := range memberships {
		var spec wow.PackSpec
		if err := json.Unmarshal([]byte(m.SpecJSON), &spec); err != nil {
			return nil, fmt.Errorf("pack %s: %w", m.Pack, err)
		}
		for _, name := range spec.Schedules {
			names[name] = true
		}
	}
	return names, nil
}

// Pack membership, rather than a per-project workflow choice, supplies these names.
func packWorkflowNamesIn(ctx context.Context, q database.Queryer, projectID ids.ID) (map[string]bool, error) {
	memberships, err := ProjectPacksIn(ctx, q, projectID)
	if err != nil {
		return nil, err
	}
	names := map[string]bool{}
	for _, m := range memberships {
		var spec candidatePackSpec
		if err := json.Unmarshal([]byte(m.SpecJSON), &spec); err != nil {
			return nil, fmt.Errorf("pack %s: %w", m.Pack, err)
		}
		for _, name := range spec.Workflows {
			names[name] = true
		}
	}
	return names, nil
}
