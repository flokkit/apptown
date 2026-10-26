package deployment

import (
	"context"
	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

// StandingConsentSelection is an observation for bounded human delegation, not
// release admission or health evidence. Release claims retain all normal guards.
type StandingConsentSelection struct {
	Resource, PromotionResource access.Resource
	PromotionRequired           bool
}

func ReadStandingConsentSelection(ctx context.Context, q database.Queryer, projectID, deploymentID, targetID ids.ID) (StandingConsentSelection, error) {
	var out StandingConsentSelection
	dep, err := deploymentRecord(deploymentsql.New(q.SQL()).DeploymentByID(ctx, deploymentID))
	if err != nil {
		return out, database.Missing(err)
	}
	if dep.ProjectID != projectID || dep.TargetID != targetID || dep.Kind != Standing {
		return out, fault.New(fault.Conflict, "standing delivery requires the exact standing environment and target selected for this project")
	}
	target, err := targetRecord(deploymentsql.New(q.SQL()).TargetByID(ctx, targetID))
	if err != nil {
		return out, database.Missing(err)
	}
	if err = target.Status.Available(); err != nil {
		return out, err
	}
	if dep.RestoreCheck != "" {
		return out, fault.New(fault.Conflict, "the selected environment is held after restore")
	}
	out.Resource, err = project.AuthorityResourceIn(ctx, q, projectID)
	if err != nil {
		return out, err
	}
	out.PromotionResource = out.Resource
	out.PromotionResource.Type = "release-target"
	out.PromotionResource.ID = dep.Name
	out.PromotionRequired = dep.needsPromotion()
	return out, nil
}
