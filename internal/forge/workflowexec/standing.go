package workflowexec

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net"
	"strings"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/forge/deployment"
)

// reserveDeploymentPort returns the deployment's stable host port,
// allocating one on first use of an explicitly bound deployment. Two deploys
// racing for an unallocated port both listen on a free port; one update wins,
// and the other reads the winner's port. A constraint cannot reserve an OS
// port, so the port is one the kernel just handed out; if docker then finds
// it taken, the boot fails with that reason and the next deploy allocates
// afresh through releasePortIfTaken.
func (a *Shipping) reserveDeploymentPort(ctx context.Context, projectID ids.ID, deployment, container string) (int, error) {
	e, err := a.deployment.Deployments.ByName(ctx, projectID, deployment)
	if err != nil || e.TargetID.IsZero() {
		return 0, fmt.Errorf("reserving a port requires an explicitly bound deployment: %w", err)
	}
	if e.HostPort > 0 {
		return e.HostPort, nil
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	candidate := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return a.deployment.Deployments.ReserveHostPort(ctx, projectID, deployment, container, candidate)
}

// releasePortIfTaken: when a boot failed because the reserved port was in
// use by something else, forget the reservation so the next deploy
// allocates a new one.
func (a *Shipping) releasePortIfTaken(ctx context.Context, projectID ids.ID, deployment string, bootErr error) {
	if bootErr == nil || !strings.Contains(bootErr.Error(), "address already in use") {
		return
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return
	}
	fresh := l.Addr().(*net.TCPAddr).Port
	l.Close()
	if err := a.deployment.Deployments.ResetHostPort(ctx, projectID, deployment, fresh); err != nil {
		log.Printf("deployment %s: re-reserving its port after a bind failure: %v", deployment, err)
	}
}

// deploymentDataClass is what a deployment declares it holds
// (Deployments.DataClassOf): unknown protects its data.
func (a *Shipping) deploymentDataClass(ctx context.Context, projectID ids.ID, deployment string) string {
	return a.deployment.Deployments.DataClassOf(ctx, projectID, deployment)
}

// landingDeployment is the standing deployment a landing deploys (main),
// created with its attributes on first use on the target the deployment
// owner names: the built-in host, or without local Docker the one attached
// Docker host offered to the project.
// A target this forge cannot release to is refused here, before the
// landing reserves a port or changes the deployment in any way, whether
// the deployment exists already or would be created.
func (a *Shipping) landingDeployment(ctx context.Context, projectID ids.ID, name string) (*deployment.Deployment, error) {
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
