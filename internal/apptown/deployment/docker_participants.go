package deployment

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/docker"
)

// ClaimedExecution is Work's admitted execution, never transport input. The
// participant checks its kind and uses its identity; Work owns claim/finish and
// their transaction guards. Implementations belong to trusted composition.
type ClaimedExecution interface {
	ClaimedStep(kind string) (projectID, runID, stepID ids.ID, err error)
}

type LandingInput struct {
	Log       deploy.Logf // Optional phase progress; no environment values or command output.
	Start     Start
	Source    deploy.Source
	Services  []deploy.ServiceSpec
	HostPort  int
	DataClass string
	// Bundle is the digest bundle the landing committed before its deploy
	// (PublishRelease), whose images the host pulls by digest and runs,
	// building nothing; Start.Inputs.BundleID names it. nil builds the
	// source on the host.
	Bundle *ReleaseBundle
}
type LandingResult struct {
	Execution ExecutionResult
	Boot      LandingBoot
	Evidence  LandingEvidence
	// Docker is the daemon the landing booted on, and TargetID its target:
	// where the images it built are, and the only place they are.
	Docker   docker.Scope
	TargetID ids.ID
}

// RunLanding admits the selected deployment, executes it and settles through
// Runner. A replay or stale generation never authorizes another Docker effect.
func (s *Service) RunLanding(ctx context.Context, claim ClaimedExecution, in LandingInput) (LandingResult, error) {
	fail := func(err error) (LandingResult, error) {
		return LandingResult{}, &RunError{Stage: Admission, Cause: err}
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if claim == nil {
		return fail(errors.New("landing requires a claimed Work execution"))
	}
	projectID, runID, stepID, err := claim.ClaimedStep("deploy")
	if err != nil {
		return fail(err)
	}
	if projectID.IsZero() || runID.IsZero() || stepID.IsZero() || in.Start.Inputs.ReleaseRun != runID || in.Source.Contract == nil || in.Source.Workspace == "" || in.Source.Revision != in.Start.Inputs.Revision {
		return fail(errors.New("invalid landing execution inputs"))
	}
	if in.Bundle != nil && (!in.Bundle.ByDigest() || in.Bundle.ID != in.Start.Inputs.BundleID || in.Bundle.ProjectID != projectID || in.Bundle.ContractRevision != in.Source.Revision) {
		return fail(errors.New("a landing runs the digest bundle its inputs name, of its own commit"))
	}
	dep, err := s.Deployments.ByID(ctx, in.Start.DeploymentID)
	if err != nil {
		return fail(err)
	}
	if dep.ProjectID != projectID || dep.TargetID != in.Start.TargetID {
		return fail(errors.New("landing scope changed"))
	}
	p, err := s.projects.ByID(ctx, projectID)
	if err != nil {
		return fail(err)
	}
	if p.Slug != in.Source.Slug {
		return fail(errors.New("landing source belongs to another project"))
	}
	target, err := s.Targets.ByID(ctx, dep.TargetID)
	if err != nil {
		return fail(err)
	}
	if err := s.RefusesLanding(ctx, projectID, target); err != nil {
		return fail(err) // before anything is built
	}
	// The landing runs the deployment's profile and overrides over the
	// committed contract, recorded with the generation, as every release
	// does (profiles.go).
	committed := in.Source.Contract
	effective, applied, err := s.effectiveContract(ctx, dep, in.Source.Workspace, committed)
	if err != nil {
		return fail(err)
	}
	in.Services, applied.Services, err = SuppliedServiceDefaults(effective, in.Services)
	if err != nil {
		return fail(err)
	}
	in.Start.Inputs.Applied = applied
	dk, err := s.hosts.DockerScope(ctx, target)
	if err != nil {
		return fail(err)
	}
	names, err := dep.HostNames()
	if err != nil {
		return fail(err)
	}
	request := docker.BootRequest{Allowed: docker.AuthorisedWhenRequested, Docker: dk, Name: names.App(), StackID: names.Stack(), Workspace: in.Source.Workspace, Slug: p.Slug, ServingLabel: p.ServingLabel, Deployment: dep.Name, DataClass: in.DataClass, HostPort: in.HostPort, Persistent: true, Services: in.Services, Probe: 90 * time.Second, PageCheck: true, Revision: in.Source.Revision}
	owner := s.Deployments.OwnerOf(dep)
	request.Storage = s.Deployments.StorageOf(dep)
	request.Log = in.Log
	request.Record = func(o deploy.Owned) error { return owner.Created(ctx, o) }
	if request.Owned, err = s.Deployments.Owned(ctx, dep.ID); err != nil {
		return fail(fmt.Errorf("reading what the deployment owns: %w", err))
	}
	if in.Bundle != nil {
		login, err := s.env.BundleLogin(ctx, projectID, in.Bundle)
		if err != nil {
			return fail(fmt.Errorf("the credential the release's images are pulled with: %w", err))
		}
		request.Recorded = &docker.RecordedImages{Parts: in.Bundle.Parts, Init: in.Bundle.Init, Login: login, Scratch: s.hosts.BootScratch()}
	}
	// An attached host is reached at that host, main too: this AppTown's
	// routes serve only its own host.
	routes := s.env.RoutesFor(ctx, projectID)
	reached := func(port int) string {
		reach, err := dockerReach(routes, target, p.ServingLabel, dep.Name, port)
		if err != nil {
			return ""
		}
		return reach.URL
	}
	reach, err := dockerReach(routes, target, p.ServingLabel, dep.Name, in.HostPort)
	if err != nil {
		return fail(err)
	}
	origin, err := docker.ExactOrigin(reach.URL)
	if err != nil {
		return fail(err)
	}
	request.ExactOrigins = &origin
	request.VisitorHost = docker.VisitorHost(reach.URL)
	executor := landingDeploymentExecution{label: dep.Label(), target: request, contract: effective, artifact: in.Start.Inputs.ArtifactHash, boot: s.env.Host(projectID, s.env.NeedCredentials(NeedSecretsScope(dep.ID)), func(slug string) KeyOwner { return deploymentKeys(s.db, s.clock, dep, slug) }).Boot,
		url: reached}
	// ctx bounds what comes before admission. Once admitted, the attempt
	// runs to its own outcome: the step's interruption (its run abandoned)
	// does not cut the deploy short into an outcome no one knows, which
	// would leave the deployment refusing releases until it is observed.
	// The step returns once the deploy has settled. AppTown stopping leaves
	// the attempt running, for startup recovery. Reassigned here, ctx
	// detaches the callbacks above that close over it too.
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	ctx = context.WithoutCancel(ctx)
	result, err := (Runner{Store: s.Outcomes, Applied: s.applied}).Run(ctx, in.Start, executor.execute)
	out := LandingResult{Execution: result, Boot: executor.result, Docker: dk, TargetID: target.ID}
	if err == nil && result.Created && result.Outcome == Applied {
		out.Evidence = executor.evidence(ctx)
		s.clearCommittedOverrides(ctx, dep, committed, applied, in.Source.Revision, func(format string, args ...any) {
			log.Printf("landing on %s: "+format, append([]any{dep.Name}, args...)...)
		})
	}
	return out, err
}
