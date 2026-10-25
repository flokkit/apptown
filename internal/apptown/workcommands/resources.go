package workcommands

import (
	"context"
	"fmt"
	"github.com/flokkit/apptown/internal/apptown/project"
	"os"
	"path/filepath"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/preview"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/gitexec"
)

// Resources performs cleanup for an explicitly completed or abandoned scope.
// Persistent deployment resources and saved conversations are not selected.
type Resources struct {
	DB       *database.DB
	Previews *preview.Service
	DataDir  string
}

// Run tears down one run's preview (called on run
// completion/failure): its app container, the data volume that dies with it
// (deployed deployments' volumes — <prefix>data-<prefix>env-* — are the
// persistence and are never touched), and the throwaway stack of each of
// its preview steps. It used to ignore its argument and sweep every preview
// stack on the box, so finishing one run tore down another run's live
// preview services.
// A removal that is not confirmed is an error, so the job that asked tries
// again.
func (a Resources) Run(ctx context.Context, runID ids.ID) error {
	stepIDs, err := workflow.PreviewStepIDsIn(context.WithoutCancel(ctx), a.DB, runID)
	if err != nil {
		return fmt.Errorf("run %d: reading its preview steps: %w", runID, err)
	}
	return a.Previews.DiscardRunPreview(ctx, runID, stepIDs)
}

// Change removes a Change's worktree once it is shipped or
// abandoned. The branch stays (merged or forensic); the workspace is cattle.
func (a Resources) Change(ctx context.Context, changeID ids.ID) error {
	change, err := workflow.ReadChangeIn(ctx, a.DB, changeID)
	if err != nil {
		return err
	}
	p, err := project.ByIDIn(ctx, a.DB, change.ProjectID)
	if err != nil {
		return err
	}
	repo, err := p.Repository(a.DataDir)
	if err != nil {
		return err
	}
	previewErr := a.Previews.DiscardChangePreview(ctx, changeID)
	wt := filepath.Join(a.DataDir, "workspaces", fmt.Sprintf("change-%d", changeID))
	if err := os.RemoveAll(wt); err != nil {
		return fmt.Errorf("removing change %d workspace: %w", changeID, err)
	}
	// The snapshots its previews of history served go with the preview,
	// once it is gone; a removal not confirmed is tried again with this job.
	if previewErr != nil {
		return previewErr
	}
	if err := os.RemoveAll(filepath.Join(a.DataDir, "workspaces", fmt.Sprintf("asof-change-%d", changeID))); err != nil {
		return fmt.Errorf("removing change %d's preview snapshots: %w", changeID, err)
	}
	// Pruning registrations is safe to repeat. Non-repository projects have
	// no registrations to prune; the forensic branch itself is retained.
	if _, err := os.Stat(filepath.Join(repo, ".git")); err == nil {
		if _, err := gitexec.Run(ctx, repo, "worktree", "prune"); err != nil {
			return err
		}
	}
	return nil
}
