package deployment

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
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
	slug, name, ok := strings.Cut(key, "/")
	if !ok {
		return nil, jobs.Permanent(fmt.Errorf("a rebuild key is project/deployment, got %q", key))
	}
	var projectID ids.ID
	if p, err := w.Projects.BySlug(ctx, slug); err == nil {
		projectID = p.ID
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
	plan, err := w.Service.PrepareRebuild(ctx, dep, selected.Deployment, selected.Generation)
	if errors.Is(err, ErrRebuildUnselected) || errors.Is(err, ErrReleaseRefused) {
		return nil, jobs.Permanent(err)
	}
	if err != nil {
		return nil, err
	}
	if plan.Skipped != "" {
		out := map[string]any{"skipped": plan.Skipped}
		if !plan.Attempt.IsZero() {
			out["attempt"], out["outcome"] = plan.Attempt, plan.Outcome
		}
		return out, nil
	}
	// The recorded source, restored from the project's repository: the
	// caller's, as every deploy's source is.
	art, cleanup, err := docker.RestoreSource(ctx, filepath.Join(w.DataDir, "workspaces", "project-"+slug), slug, plan.Inputs.DockerSource)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, jobs.Permanent(err)
	}
	defer cleanup()
	result, err := w.Service.Rebuild(ctx, dep, plan, art)
	if result.ProviderErr != nil && w.Reporter != nil {
		w.Reporter.Report("deployment-rebuild", "deployments/"+slug+"/"+name, result.ProviderErr.Error(), map[string]string{"project": slug, "deployment": name}, "")
	}
	if errors.Is(err, ErrRebuildUnselected) || errors.Is(err, ErrReleaseRefused) {
		return nil, jobs.Permanent(err)
	}
	if err != nil {
		return nil, err
	}
	if result.Skipped != "" {
		return map[string]any{"skipped": result.Skipped}, nil
	}
	return map[string]any{"url": result.URL, "attempt": result.Attempt, "outcome": result.Outcome}, nil
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
	result, err := w.Service.Recover(ctx, id)
	return map[string]any{"attempt": result.Attempt, "outcome": result.Outcome, "reason": result.Reason}, err
}
