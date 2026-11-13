package verification

import (
	"context"
	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/ids"
	"net/url"
	"strings"
)

// Portfolio reads standing coverage and deployment facts for consent.
type Portfolio struct {
	Adoptions *adoption.Service
	Releases  *deployment.Service
	DataDir   string
	Work      *workflow.Service
}

func (p Portfolio) StandingHold(ctx context.Context, q database.Queryer, pid ids.ID) (string, error) {
	holds, err := p.Releases.HeldBackIn(ctx, q, pid)
	if err != nil {
		return "", err
	}
	reasons := make([]string, 0, len(holds))
	for _, hold := range holds {
		reasons = append(reasons, hold.String())
	}
	return strings.Join(reasons, "; "), nil
}
func (p Portfolio) StandingPortfolio(ctx context.Context, q database.Queryer, pid ids.ID, _ workflow.StandingCategories) ([]workflow.StandingProjectPreview, error) {
	owners := []project.RepositoryOwner{{Ref: project.RepositoryRef{ID: pid}}}
	if pid.IsZero() {
		var err error
		owners, err = project.RepositoryOwnersIn(ctx, q)
		if err != nil {
			return nil, err
		}
	}
	out := []workflow.StandingProjectPreview{}
	for _, owner := range owners {
		if owner.Retained || owner.Lifecycle != "" {
			continue
		}
		item, err := project.ByIDIn(ctx, q, owner.Ref.ID)
		if err != nil {
			return nil, err
		}
		// A project with no repository yet has no main to read or update.
		if item.DefaultBranch == "" || (pid.IsZero() && !item.Purpose.Schedules()) {
			continue
		}
		preview := workflow.StandingProjectPreview{ProjectID: item.ID, Project: item.Slug, Deployments: []workflow.StandingEnvironment{}, NeedsPackUpgrade: []string{}, PackOffers: []string{}, BaseImageFindings: []string{}}
		revision, err := adoption.CurrentReceiptIn(ctx, q, item.ID)
		if err != nil {
			return nil, err
		}
		ds, err := p.Adoptions.Fixes(ctx, adoption.Source{ProjectID: item.ID, Revision: revision})
		if err != nil {
			return nil, err
		}
		repo, err := item.Repository(p.DataDir)
		if err != nil {
			return nil, err
		}
		contract, err := docker.ReadContractAt(ctx, repo, item.DefaultBranch)
		if err != nil {
			return nil, err
		}
		missing, err := updateCoverage(contract, ds)
		if err != nil {
			return nil, err
		}
		preview.NoMajorCommand, err = partsWithoutMajorCommand(contract, ds)
		if err != nil {
			return nil, err
		}
		preview.Covered = len(missing) == 0
		if !preview.Covered {
			var tools []string
			for _, part := range missing {
				tools = append(tools, part.Tools...)
			}
			preview.NeedsPackUpgrade, err = adoption.PacksWithTools(ctx, q, item.ID, tools)
			if err != nil {
				return nil, err
			}
			for _, pack := range preview.NeedsPackUpgrade {
				preview.PackOffers = append(preview.PackOffers, "/packs/"+url.PathEscape(pack)+"/projects")
			}
		}
		preview.OnlyChecksStarts = onlyChecksStarts(contract)
		deps, err := p.Releases.Deployments.ForProject(ctx, item.ID)
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
		if p.Work != nil {
			readCtx := access.WithCaller(ctx, access.Caller{Principal: access.Principal{Kind: "instrument", Name: "system", Instrument: access.SystemInstrument, Via: "engine", ProjectID: item.ID}, Verified: true})
			scans, err := p.Work.LatestMainScans(readCtx, item.ID)
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
