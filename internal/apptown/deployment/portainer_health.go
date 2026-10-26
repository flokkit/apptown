package deployment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/portainer"
)

// portainerDiagnostic gives one admitted sample a private proxy over exactly
// the verified credential/scope snapshot supplied by its driver. The driver
// checks current authority and scope before effects; this never rereads secrets
// midway through a Docker request or judges the resulting health predicate.
func (p *Providers) portainerDiagnostic(ctx context.Context, cfg portainer.Target) (portainer.Diagnostic, func() error, error) {
	if cfg.Scope == nil || cfg.Scope.Version != 1 || p.hosts == nil {
		return portainer.Diagnostic{}, nil, errors.New("Portainer diagnostic has no verified transport")
	}
	env, err := portainer.DataReach(ctx, &cfg)
	if err != nil {
		return portainer.Diagnostic{}, nil, err
	}
	if env.ID != cfg.Scope.EndpointID || env.Type != 1 {
		return portainer.Diagnostic{}, nil, errors.New("Portainer diagnostic requires the pinned standalone endpoint")
	}
	dir, err := os.MkdirTemp("/tmp", "apptown-health-proxy-")
	if err != nil {
		return portainer.Diagnostic{}, nil, err
	}
	scope := *cfg.Scope
	cfg.Scope = &scope
	proxy, err := portainer.StartDockerProxy(dir, env, func(context.Context) (*portainer.Target, error) { return &cfg, nil })
	if err != nil {
		return portainer.Diagnostic{}, nil, errors.Join(err, os.RemoveAll(dir))
	}
	dk := p.hosts.settings.Scope.WithHost(proxy.Host(), "", "")
	dk.NoStreams = true
	close := func() error { return errors.Join(proxy.Close(), os.RemoveAll(dir)) }
	command := func(ctx context.Context, env []string, args ...string) ([]byte, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		out, err := dk.Command(ctx, docker.Command{Args: args, Env: env, Timeout: 30 * time.Second})
		if err != nil {
			return out, fmt.Errorf("Portainer diagnostic transport: %w", err)
		}
		return out, nil
	}
	return portainer.Diagnostic{Command: command}, close, nil
}

// requireRecoveryReadiness binds diagnostic recovery to the original request,
// re-resolved under current law. The request row's autocommit read ends before
// permission checks or any provider I/O; no recovery operator is substituted.
func (s *Service) requireRecoveryReadiness(ctx context.Context, at Attempt) error {
	in, err := s.Outcomes.InputsFor(ctx, at)
	if err != nil {
		return err
	}
	if in.ReleaseID.IsZero() || in.ArtifactHash == "" {
		return errors.New("Portainer recovery lacks admitted release identity")
	}
	row, err := deploymentsql.New(s.db.SQL()).RequestedReleaseForUpdate(ctx, in.ReleaseID)
	if err != nil {
		return err
	}
	if row.Release.DeploymentID != at.DeploymentID {
		return errors.New("Portainer recovery request belongs to another deployment")
	}
	dep, err := s.Deployments.ByID(ctx, at.DeploymentID)
	if err != nil {
		return err
	}
	if dep.TargetID != row.ReleaseRequest.TargetID || dep.DesiredGeneration != at.Generation {
		return errors.New("Portainer recovery placement changed")
	}
	if err := dep.HeldAfterRestore(); err != nil {
		return err
	}
	if err := project.RefuseUnlessActive(ctx, s.db, dep.ProjectID); err != nil {
		return err
	}
	target, err := s.Targets.ByID(ctx, dep.TargetID)
	if err != nil {
		return err
	}
	if err := matchesProviderBinding(target, row.ReleaseRequest.ProviderBindingJson); err != nil {
		return err
	}
	var raw json.RawMessage
	if row.ReleaseRequest.AuthorityJson != nil {
		raw = *row.ReleaseRequest.AuthorityJson
	}
	captured, err := releasePrincipal(raw)
	if err != nil {
		return err
	}
	return s.requireReleaseAuthority(ctx, captured, dep.ID, row.ReleaseRequest.RequirePromotion)
}

func (d *portainerRelease) maintainReadiness(ctx context.Context, p deploy.Placement, artifact string, allowed func(context.Context) error) (deploy.Observation, error) {
	d.ExpectedArtifact, d.Allowed = artifact, allowed
	return d.Sample(ctx, p)
}
