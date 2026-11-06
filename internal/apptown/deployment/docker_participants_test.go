package deployment

import (
	"context"
	"errors"
	"fmt"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/ids"
)

// Provider fixtures prepare generations through the same execution operation
// used by queued releases. There is no production workflow deploy executor.
type LandingInput struct {
	Stage     func(deploy.DeployStage)
	Log       deploy.Logf
	Start     Start
	Source    deploy.Source
	Services  []deploy.ServiceSpec
	HostPort  int
	DataClass string
	Bundle    *ReleaseBundle
}
type LandingBoot struct {
	Boot docker.BootResult
	Err  error
	URL  string
}
type LandingResult struct {
	Execution   ExecutionResult
	Boot        LandingBoot
	Docker      docker.Scope
	TargetID    ids.ID
	parts, init map[string]string
}

func (r LandingResult) Images(context.Context) (map[string]string, map[string]string) {
	return r.parts, r.init
}

func (s *Service) RunLanding(ctx context.Context, claim ClaimedExecution, in LandingInput) (LandingResult, error) {
	var out LandingResult
	if claim == nil {
		return out, errors.New("fixture requires claimed execution")
	}
	project, run, _, err := claim.ClaimedStep("deploy")
	if err != nil {
		return out, err
	}
	if project.IsZero() || run.IsZero() || run != in.Start.Inputs.ReleaseRun || in.Source.Contract == nil {
		return out, errors.New("invalid fixture execution")
	}
	dep, err := s.Deployments.ByID(ctx, in.Start.DeploymentID)
	if err != nil {
		return out, err
	}
	if dep.ProjectID != project || dep.TargetID != in.Start.TargetID {
		return out, errors.New("fixture placement changed")
	}
	target, err := s.Targets.ByID(ctx, dep.TargetID)
	if err != nil {
		return out, err
	}
	if err = s.refusesLanding(ctx, project, target); err != nil {
		return out, err
	}
	out.TargetID = target.ID
	out.Docker, err = s.hosts.DockerScope(ctx, target)
	if err != nil {
		return out, err
	}
	source := in.Source
	effective, applied, err := s.effectiveContract(ctx, dep, source.Workspace, source.Contract)
	if err != nil {
		return out, err
	}
	source.Contract = effective
	driver, err := s.providers.Driver(ctx, target, dep)
	if err != nil {
		return out, err
	}
	fitted, err := applied.Fitted(ctx, driver, effective)
	if err != nil {
		return out, err
	}
	if in.Bundle != nil {
		source.Images, source.Init = in.Bundle.Parts, in.Bundle.Init
		source.Published = in.Bundle.ByDigest()
		source.Platform = in.Bundle.BuiltFrom.Platform
	} else {
		host := s.env.host(project, nil, nil)
		source.Images, source.Init, err = host.BuildImages(ctx, out.Docker, fmt.Sprintf("fixture-%s", run), source.Workspace, effective)
		if err != nil {
			return out, err
		}
	}
	out.parts, out.init = source.Images, source.Init
	dep.HostPort = in.HostPort
	if in.HostPort != 0 {
		names, e := dep.HostNames()
		if e != nil {
			return out, e
		}
		if _, e = s.Deployments.ReserveHostPort(ctx, dep.ProjectID, dep.Name, names.App(), in.HostPort); e != nil {
			return out, e
		}
	}
	draft := ReleaseDraft{Source: ReleaseFromBranch, ProjectID: project, DeploymentID: dep.ID, Deployment: dep.Name, RunID: run, Revision: source.Revision, Actor: "fixture"}
	if in.Bundle != nil {
		draft.Source = ReleaseFromBundle
		draft.BundleID = in.Bundle.ID
	}
	rel, err := s.Releases.Start(ctx, draft, "provider fixture")
	if err != nil {
		return out, err
	}
	art := deploy.Artifact{Kind: "docker-source", Body: []byte(in.Start.Inputs.DockerSource), Hash: in.Start.Inputs.ArtifactHash, Source: &source}
	req := preparedRelease{source: source, committed: in.Source.Contract, applied: fitted, artifact: &art, bundle: in.Bundle, actor: "fixture", releaseRun: run, allowed: docker.AuthorisedWhenRequested}
	out.Execution, err = s.execute(ctx, rel, dep, target, req)
	if err == nil {
		recorded, e := s.Releases.ByNumber(context.WithoutCancel(ctx), project, rel.Number)
		if e != nil {
			return out, e
		}
		if recorded.State != Released {
			return out, fmt.Errorf("fixture release %s is %s after outcome %s", rel.ID, recorded.State, out.Execution.Outcome)
		}
	}
	out.Boot.Err = err
	if current, e := s.Deployments.ByID(context.WithoutCancel(ctx), dep.ID); e == nil {
		out.Boot.URL = current.URL
		out.Boot.Boot.HostPort = current.HostPort
	}
	return out, err
}
