package workflow

import (
	"context"

	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/scm"
)

// BriefDiff is the brief's account of the change's committed diff, base to
// branch: its files and changed lines, that it changes nothing, or that it
// could not be read. It is never left out silently: the approver accepts
// the diff, and "could not be read" is not "no changes".
func (s *Service) BriefDiff(ctx context.Context, inputs ApprovalBriefInputs) string {
	ch := inputs.Change
	if ch == nil {
		return ""
	}
	if ch.Branch == "" {
		return "the change has no branch yet"
	}
	source, err := ReadSourceIn(ctx, s.db, RunRef{ID: inputs.Run.ID, ProjectID: inputs.Run.ProjectID})
	if err != nil {
		return "could not be read: " + err.Error()
	}
	hubPath, err := (project.RepositoryRef{ID: source.ProjectID, Slug: source.Slug}).Path(s.dataDir)
	if err != nil {
		return "could not be read: " + err.Error()
	}
	repo, err := scm.ComparisonRepository(ctx, hubPath, (Layout{DataDir: s.dataDir}).ChangeWorkspace(ch.ID))
	if err != nil {
		return "could not be read: " + err.Error()
	}
	hub, err := source.Repository(s.dataDir)
	if err != nil {
		return "could not be read: " + err.Error()
	}
	comparison, err := scm.ReadChangeDiff(ctx, hub, repo, source.DefaultBranch, ch.BaseSHA, ch.Branch)
	if err != nil {
		return "could not be read: " + err.Error()
	}
	if comparison.Empty {
		return "the branch changes nothing"
	}
	files, err := comparison.Files(ctx, repo)
	if err != nil {
		return "could not be read: " + err.Error()
	}
	return files
}
