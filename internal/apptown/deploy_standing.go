package apptown

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/verification"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/database"
	"log"
	"net/url"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/deploy/docker"
)

// EnsureDeployments reconciles recorded previews against the hosts at boot.
// A standing deployment whose container is gone is repaired by its host's
// reader (deployment.HostReaders).
func (a *App) EnsureDeployments(ctx context.Context) {
	go a.ensureDeploymentsOnce(ctx)
	// What records name and was made before the installation's mark gets
	// it, once a boot (deployment.Providers.MarkRecorded).
	if err := infrastructureChecksFor(a).request(ctx); err != nil {
		log.Printf("infrastructure: scheduling checks at startup: %v", err)
	}
	if _, err := a.Jobs().Enqueue(ctx, deployment.MarkInstallationJob, "boot", nil, time.Time{}); err != nil {
		log.Printf("deployments: asking to mark recorded resources: %v", err)
	}
}

// ensureDeploymentsOnce forgets previews whose container is gone or stopped.
// Deployment decides; AppTown says what is running.
func (a *App) ensureDeploymentsOnce(ctx context.Context) {
	a.Deployment.BootReconcile(ctx,
		func(targetID ids.ID, container string) (deployment.ContainerPresence, error) {
			dk := a.docker
			if !targetID.IsZero() {
				scoped, err := a.hosts.DockerScopeFor(ctx, targetID)
				if err != nil {
					return deployment.ContainerGone, err
				}
				dk = scoped
			}
			rt, err := docker.Inspect(ctx, dk, container)
			if errors.Is(err, docker.ErrContainerAbsent) {
				return deployment.ContainerGone, nil
			}
			if err != nil {
				return deployment.ContainerGone, err
			}
			if rt.Running {
				return deployment.ContainerRunning, nil
			}
			return deployment.ContainerStopped, nil
		})
}

type standingPortfolio struct {
	adoptions *adoption.Service
	releases  *deployment.Service
	dataDir   string
	work      *workflow.Service
}

func (p standingPortfolio) StandingHold(ctx context.Context, q database.Queryer, pid ids.ID) (string, error) {
	holds, err := p.releases.HeldBackIn(ctx, q, pid)
	if err != nil {
		return "", err
	}
	reasons := make([]string, 0, len(holds))
	for _, hold := range holds {
		reasons = append(reasons, hold.String())
	}
	return strings.Join(reasons, "; "), nil
}
func (p standingPortfolio) StandingPortfolio(ctx context.Context, q database.Queryer, pid ids.ID, _ workflow.StandingCategories) ([]workflow.StandingProjectPreview, error) {
	var projects []project.Project
	if pid.IsZero() {
		owners, err := project.RepositoryOwnersIn(ctx, q)
		if err != nil {
			return nil, err
		}
		for _, owner := range owners {
			if owner.DefaultBranch == "" || owner.Retained || owner.Lifecycle != "" {
				continue
			}
			item, err := project.ByIDIn(ctx, q, owner.Ref.ID)
			if err != nil {
				return nil, err
			}
			if item.Purpose.Schedules() {
				projects = append(projects, item)
			}
		}
	} else {
		item, err := project.ByIDIn(ctx, q, pid)
		if err != nil {
			return nil, err
		}
		// A project with no repository yet has no main to read or update; it
		// is outside coverage, exactly as the instance listing above skips it.
		if item.DefaultBranch != "" {
			projects = append(projects, item)
		}
	}
	out := []workflow.StandingProjectPreview{}
	for _, item := range projects {
		preview := workflow.StandingProjectPreview{ProjectID: item.ID, Project: item.Slug, Deployments: []workflow.StandingEnvironment{}, NeedsPackUpgrade: []string{}, PackOffers: []string{}, BaseImageFindings: []string{}}
		revision, err := adoption.CurrentReceiptIn(ctx, q, item.ID)
		if err != nil {
			return nil, err
		}
		ds, err := p.adoptions.Fixes(ctx, adoption.Source{ProjectID: item.ID, Revision: revision})
		if err != nil {
			return nil, err
		}
		repo, err := item.Repository(p.dataDir)
		if err != nil {
			return nil, err
		}
		contract, err := docker.ReadContractAt(ctx, repo, item.DefaultBranch)
		if err != nil {
			return nil, err
		}
		missing, err := verification.UpdateCoverage(contract, ds)
		if err != nil {
			return nil, err
		}
		preview.NoMajorCommand, err = verification.PartsWithoutMajorCommand(contract, ds)
		if err != nil {
			return nil, err
		}
		preview.Covered = len(missing) == 0
		preview.Group = "covered"
		if !preview.Covered {
			preview.Group = "needs a pack upgrade"
			packs, err := adoption.ProjectPacksIn(ctx, q, item.ID)
			if err != nil {
				return nil, err
			}
			for _, pack := range packs {
				var spec wow.PackSpec
				if err := json.Unmarshal([]byte(pack.SpecJSON), &spec); err != nil {
					return nil, err
				}
				needed := false
				for _, part := range missing {
					for _, tool := range part.Tools {
						if _, ok := spec.Tools[tool]; ok {
							needed = true
						}
					}
				}
				if needed {
					preview.NeedsPackUpgrade = append(preview.NeedsPackUpgrade, pack.Pack)
					preview.PackOffers = append(preview.PackOffers, "/packs/"+url.PathEscape(pack.Pack)+"/projects")
				}
			}
		}
		preview.OnlyChecksStarts = true
		for _, part := range contract.Parts {
			if len(part.Test) > 0 {
				preview.OnlyChecksStarts = false
			}
		}
		deps, err := p.releases.Deployments.ForProject(ctx, item.ID)
		if err != nil {
			return nil, err
		}
		for _, dep := range deps {
			if dep.Feed == "main" {
				preview.FollowsMain = append(preview.FollowsMain, workflow.StandingEnvironment{DeploymentID: dep.ID, TargetID: dep.TargetID})
			}
			if dep.Feed == "main" && !dep.Protected() && dep.RoutineUpdates.Mode != "person" {
				preview.Deployments = append(preview.Deployments, workflow.StandingEnvironment{DeploymentID: dep.ID, TargetID: dep.TargetID, ProjectID: item.ID, Project: item.Slug, Deployment: dep.Name, Setting: dep.RoutineUpdates.Mode})
			}
		}
		if p.work != nil {
			readCtx := access.WithCaller(ctx, access.Caller{Principal: access.Principal{Kind: "instrument", Name: "system", Instrument: access.SystemInstrument, Via: "engine", ProjectID: item.ID}, Verified: true})
			scans, err := p.work.LatestMainScans(readCtx, item.ID)
			if err != nil {
				return nil, err
			}
			images := map[string]bool{}
			for _, scan := range scans.Running {
				if len(scan.ImageOnlyFindings) > 0 && scan.Subject.Digest != "" {
					images[scan.Subject.Digest] = true
				}
			}
			for digest := range images {
				preview.BaseImageFindings = append(preview.BaseImageFindings, digest)
			}
			preview.BaseImages = len(images)
		}
		out = append(out, preview)
	}
	return out, nil
}

func (standingPortfolio) PublishedWorkflow(ctx context.Context, q database.Queryer, name string) (*adoption.ResolvedWorkflow, error) {
	version, raw, err := wow.WorkflowVersionIn(ctx, q, name, 0)
	if err != nil {
		return nil, err
	}
	doc, err := schema.ParseWorkflowStored([]byte(raw))
	if err != nil {
		return nil, err
	}
	return &adoption.ResolvedWorkflow{Name: name, Version: version, Doc: doc}, nil
}
