package identity

import (
	"context"
	"fmt"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/database"
)

// projectCatalogue supplies scope facts from Project, without giving Identity
// ownership of the project table or a dependency on its authorized service.
type projectCatalogue interface {
	ProjectResources(context.Context, ids.ID) ([]access.Resource, error)
}

// VisibleProjects is a receipt-free read. It never infers visibility from an
// instance decision: instruments are narrowed before candidates are evaluated.
func (s *Service) VisibleProjects(ctx context.Context, pr access.Principal, capability access.Capability) ([]ids.ID, error) {
	return s.visibleProjects(ctx, s.db, pr, capability)
}

func (s *Service) visibleProjects(ctx context.Context, q database.Queryer, pr access.Principal, capability access.Capability) ([]ids.ID, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if pr.Name == "" {
		return nil, access.ErrPrincipalRequired
	}
	def, known := access.Definitions[capability]
	if !known || def.Risk != "read" || !access.Inherits(capability, access.ScopeProject) {
		return nil, fmt.Errorf("%s is not a project read capability", capability)
	}
	var projectID ids.ID
	if pr.Kind == "instrument" {
		switch pr.Instrument {
		case "run", "console", "eval":
			if pr.ProjectID.IsZero() {
				return nil, &access.Denied{Code: access.DenyOutsideScope, Reason: "this session requires a bound project"}
			}
		case access.SystemInstrument:
		default:
			return nil, &access.Denied{Code: access.DenyOutsideScope, Reason: "unknown project instrument"}
		}
		projectID = pr.ProjectID
	} else if pr.Kind != "user" && pr.Kind != "anonymous" {
		return nil, access.ErrPrincipalRequired
	}
	if s.cfg.Projects == nil {
		return nil, access.ErrAuthorityUnavailable
	}
	candidates, err := s.cfg.Projects.ProjectResources(ctx, projectID)
	if err != nil {
		return nil, err
	}
	// Each fact is read lazily once for this operation. The catalogue and facts
	// are not one database snapshot; the next operation reads all facts anew.
	facts := &serviceFacts{ctx: ctx, q: q, at: s.now()}
	visible := make([]ids.ID, 0, len(candidates))
	for _, resource := range candidates {
		// The catalogue is a fact source, but cannot widen an instrument's bounds.
		if resource.ProjectID.IsZero() || (!projectID.IsZero() && resource.ProjectID != projectID) {
			return nil, fmt.Errorf("invalid project catalogue scope")
		}
		decision, err := explainWithFacts(ctx, pr, access.DelegationOf(pr), access.Permission{Capability: capability, Resource: resource}, facts)
		if err != nil {
			return nil, err
		}
		if decision.Allow {
			visible = append(visible, resource.ProjectID)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return visible, nil
}
