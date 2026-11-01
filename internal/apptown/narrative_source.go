package apptown

import (
	"context"
	"fmt"

	"github.com/flokkit/apptown/internal/apptown/project"

	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow"
)

// Git and chat are observations gathered after the database snapshot is closed.
// No repository is written and no SQL lock is held during diff collection.
func narrativeDiff(ctx context.Context, dataDir string, source workflow.NarrativeSource) (narrativeEvidence, error) {
	ch := source.Change
	if ch.Branch == "" {
		return narrativeEvidence{}, fmt.Errorf("change has no branch to compare")
	}
	hub, err := (project.RepositoryRef{ID: ch.ProjectID, Slug: source.Slug}).Path(dataDir)
	if err != nil {
		return narrativeEvidence{}, err
	}
	workspace := (workflow.Layout{DataDir: dataDir}).ChangeWorkspace(ch.ID)
	repo, err := scm.ComparisonRepository(ctx, hub, workspace)
	if err != nil {
		return narrativeEvidence{}, err
	}
	comparison, err := scm.ReadChangeDiff(ctx, hub, repo, source.DefaultBranch, ch.BaseSHA, ch.Branch)
	if err != nil {
		return narrativeEvidence{}, err
	}
	if comparison.Empty {
		return narrativeEvidence{Empty: true}, nil
	}
	patch, err := comparison.Patch(ctx, repo)
	if err != nil {
		return narrativeEvidence{}, err
	}
	return narrativeEvidence{Stat: comparison.Stat, Patch: patch}, nil
}
