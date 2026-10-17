package forge

import (
	"context"
	"path/filepath"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/deploy/appcontract"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/forge/deployment"
	"flokkit.io/forge/internal/forge/project"
)

// projectSources supplies Deployment's source reads. It holds only the project
// catalog and repository location; it cannot reach runtime state or other services.
type projectSources struct {
	projects *project.Projects
	dataDir  string
}

func (d projectSources) Project(ctx context.Context, id ids.ID) (deployment.ReleaseProject, error) {
	if err := ctx.Err(); err != nil {
		return deployment.ReleaseProject{}, err
	}
	p, err := d.projects.ByID(ctx, id)
	if err != nil {
		return deployment.ReleaseProject{}, err
	}
	return deployment.ReleaseProject{Application: deployment.Application{ID: p.ID, Slug: p.Slug}, Repository: projectRepository(d.dataDir, p.Slug), OwnerGroup: p.OwnerGroup}, nil
}
func (projectSources) Contract(ctx context.Context, p deployment.ReleaseProject, revision string) (*appcontract.Contract, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	commit, err := (deploy.SelectedSource{Repository: p.Repository, Revision: revision}).Resolve(ctx)
	if err != nil {
		return nil, "", err
	}
	c, err := docker.ReadContractAt(ctx, p.Repository, commit)
	if err != nil {
		return nil, "", err
	}
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	return c, commit, nil
}

func (d projectSources) CurrentContract(ctx context.Context, slug string) (*appcontract.Contract, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c, err := docker.ReadWorkspaceContract(projectRepository(d.dataDir, slug))
	if err != nil {
		return nil, err
	}
	return c, ctx.Err()
}

func (d projectSources) Declared(project string) (deployment.Declared, bool) {
	c, err := docker.ReadWorkspaceContract(projectRepository(d.dataDir, project))
	if err != nil {
		return deployment.Declared{}, false
	}
	return deployment.Declared{KeepsData: c.PublicPart().DataDir != "", Needs: c.Needs}, true
}

func projectRepository(dataDir, slug string) string {
	return filepath.Join(dataDir, "workspaces", "project-"+slug)
}
