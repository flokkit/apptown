package deployment

import (
	"context"
	"fmt"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/ids"
)

type SourceBuild struct {
	By                         AttemptClaim
	ProjectID, ChangeID, RunID ids.ID
	Tree, Revision             string
	Contract                   *appcontract.Contract
	Raw                        []byte
	Log                        deploy.Logf
}

// BuildRelease uses a build daemon without admitting a deployment generation.
// It builds init images but never executes them or resolves runtime needs.
func (s *Service) BuildRelease(ctx context.Context, in SourceBuild) (*ReleaseBundle, error) {
	t, err := s.landingTarget(ctx, in.ProjectID, "build", Attributes{Feed: "manual", Audience: "team", DataClass: "synthetic"})
	if err != nil {
		return nil, err
	}
	if err := s.refusesLanding(ctx, in.ProjectID, t); err != nil {
		return nil, err
	}
	dk, err := s.hosts.DockerScope(ctx, t)
	if err != nil {
		return nil, err
	}
	_, _, step, err := in.By.ClaimedStep("build")
	if err != nil {
		return nil, err
	}
	host := docker.NewHost(docker.Dependencies{StaticServer: s.ToolVersions().Images.StaticServer, Registry: hostRegistry{s.env.integrations, in.ProjectID}})
	parts, init, err := host.BuildImages(ctx, dk, fmt.Sprintf("build-%s-%s-%s", in.RunID, step, ids.New(s.now())), in.Tree, in.Contract)
	if err != nil {
		return nil, err
	}
	publish, err := s.publishesLanding(ctx, in.ProjectID)
	if err != nil {
		return nil, err
	}
	if publish {
		return s.publishImages(ctx, t, parts, init, ReleasePublication{By: in.By, ChangeID: in.ChangeID, Tree: in.Revision, Contract: in.Raw, Plan: PlanRelease(in.Contract), Log: in.Log})
	}
	id, err := s.Bundles.Cut(ctx, in.ProjectID, in.Revision, parts, init, in.Raw, BundleOrigin{ChangeID: in.ChangeID, RunID: in.RunID, TargetID: t.ID, By: in.By})
	if err != nil {
		return nil, err
	}
	return s.Bundles.ByID(ctx, id)
}
