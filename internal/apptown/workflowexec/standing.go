package workflowexec

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/deployment"
)

// deploymentDataClass is what a deployment declares it holds
// (Deployments.DataClassOf): unknown protects its data.
func (a *shipping) deploymentDataClass(ctx context.Context, projectID ids.ID, deployment string) string {
	return a.deployment.Deployments.DataClassOf(ctx, projectID, deployment)
}

// landingDeployment is the standing deployment a landing deploys (main),
// created with its attributes on first use on the target the deployment
// owner names: the built-in host, or without local Docker the one attached
// Docker host offered to the project.
// A target this AppTown cannot release to is refused here, before the
// landing reserves a port or changes the deployment in any way, whether
// the deployment exists already or would be created.
func (a *shipping) landingDeployment(ctx context.Context, projectID ids.ID, name string) (*deployment.Deployment, error) {
	env, err := a.deployment.Deployments.ByName(ctx, projectID, name)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if err == nil {
		if env.TargetID.IsZero() {
			return nil, fmt.Errorf("deployment %s has no selected target", name)
		}
		t, err := a.deployment.Targets.ByID(ctx, env.TargetID)
		if err != nil {
			return nil, fmt.Errorf("deployment %s target: %w", name, err)
		}
		if err := a.deployment.RefusesLanding(ctx, projectID, t); err != nil {
			return nil, err
		}
		return env, nil
	}
	attrs := deployment.Attributes{Feed: "landing", Audience: "team", DataClass: "synthetic"}
	t, err := a.deployment.LandingTarget(ctx, projectID, name, attrs)
	if err != nil {
		return nil, err
	}
	if err := a.deployment.RefusesLanding(ctx, projectID, t); err != nil {
		return nil, err
	}
	return a.deployment.Deployments.EnsureStanding(ctx, projectID, name, t.ID, attrs)
}
