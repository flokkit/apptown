package verification

import (
	"context"
	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/ids"
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
func (p Portfolio) StandingPortfolio(ctx context.Context, q database.Queryer, _ workflow.StandingCategories) ([]workflow.StandingProjectPreview, error) {
	owners, err := project.RepositoryOwnersIn(ctx, q)
	if err != nil {
		return nil, err
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
		if item.DefaultBranch == "" || !item.Purpose.Schedules() {
			continue
		}
		preview := workflow.StandingProjectPreview{ProjectID: item.ID, Project: item.Slug, Deployments: []workflow.StandingEnvironment{}, BaseImageFindings: []string{}}
		repo, err := item.Repository(p.DataDir)
		if err != nil {
			return nil, err
		}
		contract, err := docker.ReadContractAt(ctx, repo, item.DefaultBranch)
		if err != nil {
			return nil, err
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
			scans, err := p.Work.LatestMainScans(readCtx, item.ID, 0)
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

// onlyChecksStarts reports whether no contract part declares a test command.
func onlyChecksStarts(c *appcontract.Contract) bool {
	for _, part := range c.Parts {
		if len(part.Test) > 0 {
			return false
		}
	}
	return true
}

// StandingReach reads eligibility facts without the consent preview's contract or scans.
func (p Portfolio) StandingReach(ctx context.Context, q database.Queryer, pid ids.ID) (workflow.StandingReach, error) {
	item, err := project.ByIDIn(ctx, q, pid)
	if err != nil {
		return workflow.StandingReach{}, err
	}
	out := workflow.StandingReach{Repository: item.DefaultBranch != ""}
	deps, err := p.Releases.Deployments.ForProject(ctx, pid)
	if err != nil {
		return out, err
	}
	for _, dep := range deps {
		if dep.Feed == "main" {
			out.FollowsMain = append(out.FollowsMain, workflow.StandingEnvironment{DeploymentID: dep.ID, TargetID: dep.TargetID})
		}
	}
	return out, nil
}
