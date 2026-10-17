package forge

import (
	"context"
	"fmt"
	"path/filepath"

	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/strs"
)

// Git and chat are observations gathered after the database snapshot is closed.
// No repository is written and no SQL lock is held during diff collection.
func narrativeDiff(ctx context.Context, dataDir string, source workflow.NarrativeSource) (narrativeEvidence, error) {
	ch := source.Change
	if ch.Branch == "" {
		return narrativeEvidence{}, fmt.Errorf("change has no branch to compare")
	}
	hub := filepath.Join(dataDir, "workspaces", "project-"+source.Slug)
	workspace := filepath.Join(dataDir, "workspaces", fmt.Sprintf("change-%d", ch.ID))
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
	return narrativeEvidence{Text: "DIFF STAT:\n" + strs.Truncate(comparison.Stat, 800) + "\nDIFF:\n" + strs.Truncate(patch, 6000) + "\n"}, nil
}
