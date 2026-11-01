package deployment

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/jobs"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/deploy/docker"
)

// JobWorker adapts durable job requests to Deployment's operations. It owns
// payload decoding and receipt/error mapping; River owns delivery and retry.
// This immutable wiring value has no queue lifecycle or App reference.
type JobWorker struct {
	Service  *Service
	Projects *project.Projects
	DataDir  string
	Reporter interface {
		Report(string, string, string, map[string]string, string)
	}
}

// rebuild: a standing deployment on a Docker host whose container is gone
// — the same sequence a landing deploys (needs, init, parts, app). Never a
// cloud or Portainer deployment (rebuilding one would stand up a second,
// local copy of a cloud deployment), never a preview.
func (w JobWorker) Rebuild(ctx context.Context, key string, args map[string]any) (map[string]any, error) {
	owner, name, ok := strings.Cut(key, "/")
	keyID, keyErr := ids.Parse(owner)
	if !ok || keyErr != nil {
		return nil, jobs.Permanent(fmt.Errorf("a rebuild key is project-id/deployment, got %q", key))
	}
	var projectID ids.ID
	var repositoryOwner project.RepositoryRef
	var slug string
	if p, err := w.Projects.ByID(ctx, keyID); err == nil {
		slug = p.Slug
		projectID = p.ID
		repositoryOwner = project.RepositoryRef{ID: p.ID, Slug: p.Slug}
	} else {
		if errors.Is(err, sql.ErrNoRows) {
			return map[string]any{"skipped": "the deployment is gone"}, nil
		}
		return nil, err
	}
	dep, err := w.Service.Deployments.ByName(ctx, projectID, name)
	if errors.Is(err, sql.ErrNoRows) {
		return map[string]any{"skipped": "the deployment is gone"}, nil
	}
	if err != nil {
		return nil, err
	}
	var selected struct {
		Deployment ids.ID `json:"deployment"`
		Generation int64  `json:"generation"`
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &selected); err != nil {
		return nil, jobs.Permanent(err)
	}
	plan, err := w.Service.prepareRebuild(ctx, dep, selected.Deployment, selected.Generation)
	if errors.Is(err, errRebuildUnselected) || errors.Is(err, ErrReleaseRefused) {
		w.Service.settleFill(ctx, dep, "not rebuilt: "+err.Error())
		return nil, jobs.Permanent(err)
	}
	if err != nil {
		w.settleUnlessStopping(ctx, dep, err)
		return nil, err
	}
	if plan.skipped != "" {
		w.Service.settleFill(ctx, dep, "not rebuilt: "+plan.skipped)
		out := map[string]any{"skipped": plan.skipped}
		if !plan.attempt.IsZero() {
			out["attempt"], out["outcome"] = plan.attempt, plan.outcome
		}
		return out, nil
	}
	// The recorded source, restored from the project's repository: the
	// caller's, as every deploy's source is.
	repo, err := repositoryOwner.Path(w.DataDir)
	if err != nil {
		return nil, err
	}
	art, cleanup, err := docker.RestoreSource(ctx, repo, slug, plan.inputs.DockerSource)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		w.Service.settleFill(ctx, dep, "not rebuilt: its recorded source could not be restored: "+err.Error())
		return nil, jobs.Permanent(err)
	}
	defer cleanup()
	result, err := w.Service.Rebuild(ctx, dep, plan, art)
	if result.ProviderErr != nil && w.Reporter != nil {
		w.Reporter.Report("deployment-rebuild", "deployments/"+slug+"/"+name, result.ProviderErr.Error(), map[string]string{"project": slug, "deployment": name}, "")
	}
	if errors.Is(err, errRebuildUnselected) || errors.Is(err, ErrReleaseRefused) {
		w.Service.settleFill(ctx, dep, "not rebuilt: "+err.Error())
		return nil, jobs.Permanent(err)
	}
	if err != nil {
		w.settleUnlessStopping(ctx, dep, err)
		return nil, err
	}
	switch {
	case result.Skipped != "":
		w.Service.settleFill(ctx, dep, "not rebuilt: "+result.Skipped)
	case result.Outcome == Applied:
		w.Service.settleFill(ctx, dep, "")
	default:
		why := "the rebuild ended " + string(result.Outcome)
		if result.ProviderErr != nil {
			why += ": " + result.ProviderErr.Error()
		}
		w.Service.settleFill(ctx, dep, why)
	}
	if result.Skipped != "" {
		return map[string]any{"skipped": result.Skipped}, nil
	}
	return map[string]any{"url": result.URL, "attempt": result.Attempt, "outcome": result.Outcome}, nil
}

// settleUnlessStopping settles a filling deployment's fill after a rebuild
// error: a rebuild has one attempt, so the error ends it. AppTown stopping
// is not an end: the next start's rebuild fills it.
func (w JobWorker) settleUnlessStopping(ctx context.Context, dep *Deployment, err error) {
	if ctx.Err() == nil {
		w.Service.settleFill(ctx, dep, "not rebuilt: "+err.Error())
	}
}

func (w JobWorker) Release(ctx context.Context, key string, _ map[string]any) (map[string]any, error) {
	id, err := ids.Parse(key)
	if err != nil || id.IsZero() {
		return nil, jobs.Permanent(fmt.Errorf("invalid release job key %q", key))
	}
	if err := w.Service.ExecuteRelease(ctx, id); err != nil {
		return nil, jobs.Permanent(err)
	}
	return map[string]any{"releaseId": id}, nil
}

func (w JobWorker) Recover(ctx context.Context, key string, _ map[string]any) (map[string]any, error) {
	id, err := ids.Parse(key)
	if err != nil || id.IsZero() {
		return nil, jobs.Permanent(fmt.Errorf("invalid deployment recovery identity %q", key))
	}
	result, err := w.Service.recover(ctx, id)
	return map[string]any{"attempt": result.Attempt, "outcome": result.Outcome, "reason": result.Reason}, err
}

// CheckAfterRestore is the minute's check of deployments held after a
// restore (restore_check.go): nothing to do once none is.
func (w JobWorker) CheckAfterRestore(ctx context.Context, _ string, _ map[string]any) (map[string]any, error) {
	return map[string]any{}, w.Service.checkRestoredDeployments(ctx)
}
