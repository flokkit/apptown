package forge

import (
	"context"
	"fmt"
	"slices"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/evals"
	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/forge/jobs"
	"flokkit.io/forge/internal/forge/projectlifecycle"
	"flokkit.io/forge/internal/forge/secret"
)

func projectDeletionFor(a *App) *projectlifecycle.Deletion {
	return projectlifecycle.NewDeletion(a.lifecycle, projectlifecycle.DeletionDependencies{Sessions: a.Console, Records: a.Deployment.Deployments,
		Data: a.DataProtection, GitLab: gitlabProjects{a.Secrets}, Archive: projectCleanupFor(a), Queue: cleanupQueue{}, Operations: a.operations, Now: a.cfg.clock.Now, Evals: evalSources{&a.Evals},
		Authority: access.Checked{Authority: identity.Scoped(a.Access, "project"), Projects: a.Projects}})
}

func projectCleanupFor(a *App) projectlifecycle.Cleanup {
	return projectlifecycle.Cleanup{Projects: a.Projects, Sessions: a.Console, Usage: a.Usage.Ledger, Docker: &a.docker, Previews: a.Preview, GitLab: repositoryProvider{secrets: a.Secrets, dataDir: a.cfg.dataDir}, DB: a.DB, DataDir: a.cfg.dataDir, Now: a.cfg.clock.Now}
}

// cleanupQueue queues a cleanup round in the deletion's own transaction.
type cleanupQueue struct{}

func (cleanupQueue) EnqueueCleanupIn(ctx context.Context, tx *database.Tx, projectID ids.ID, round int) error {
	return jobQueue{}.EnqueueIn(ctx, tx, jobs.Request{Operation: projectlifecycle.CleanupJob, Key: projectlifecycle.CleanupKey(projectID, round)})
}

func (cleanupQueue) EnqueuePassIn(ctx context.Context, tx *database.Tx, projectID ids.ID, after time.Time) error {
	return insertJobIn(ctx, tx, jobs.Request{Operation: projectlifecycle.DeletionPassJob, Key: fmt.Sprintf("project/%s", projectID)}, after)
}

func (cleanupQueue) LiveIn(ctx context.Context, tx *database.Tx, projectID ids.ID, round int) (bool, error) {
	keys, err := jobs.UnfinishedKeysIn(ctx, tx.Native(), projectlifecycle.CleanupJob)
	if err != nil {
		return false, err
	}
	return slices.Contains(keys, projectlifecycle.CleanupKey(projectID, round)), nil
}

// gitlabProjects looks a GitLab copy up by path for a deletion's plan.
type gitlabProjects struct{ secrets *secret.Service }

func (g gitlabProjects) ProjectID(ctx context.Context, pathNS string) (int64, error) {
	gl, err := configuredGitLab(ctx, g.secrets)
	if err != nil {
		return 0, err
	}
	p, err := gl.Project(ctx, pathNS)
	return p.ID, err
}

// evalSources reads the evals owner, built after the deletion it serves
// (evals delete their copies through it).
type evalSources struct{ evals **evals.Service }

func (e evalSources) LiveEvalsFromIn(ctx context.Context, tx *database.Tx, projectID ids.ID) ([]ids.ID, error) {
	return (*e.evals).LiveEvalsFromIn(ctx, tx, projectID)
}
