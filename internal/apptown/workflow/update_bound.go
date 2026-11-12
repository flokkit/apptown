package workflow

import (
	"context"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/ids"
)

// UpdateBoundForRun is classification input for an admitted run, never assent.
// Eligibility is read afresh from the rule owner; automatic processing remains off.
func (s StandingRules) UpdateBoundForRun(ctx context.Context, runID ids.ID) (string, error) {
	run, err := readRun(ctx, s.db, runID)
	if err != nil {
		return "", err
	}
	source, err := ReadSourceIn(ctx, s.db, RunRef{ID: runID, ProjectID: run.ProjectID})
	if err != nil {
		return "", err
	}
	root, err := readRun(ctx, s.db, source.RootRunID)
	if err != nil {
		return "", err
	}
	rows, err := workflowsql.New(s.db.SQL()).CurrentStandingRules(ctx)
	if err != nil {
		return "", err
	}
	for _, row := range rows {
		r, err := readStandingRevision(row)
		if err != nil {
			return "", err
		}
		if !r.Enabled {
			continue
		}
		matches := false
		for _, p := range r.Question.Definition.Projects {
			if p.ProjectID == run.ProjectID && p.Workflow == root.WorkflowName {
				matches = true
			}
		}
		if !matches {
			continue
		}
		status, err := s.status(ctx, r)
		if err != nil {
			return "", err
		}
		if !status.Eligible {
			continue
		}
		bound := r.Question.Definition.Categories.Dependencies
		if bound == "" && r.Question.Definition.Categories.SecurityFixes && !run.ParentRunID.IsZero() {
			bound = "minor"
		}
		if bound != "" {
			return bound, nil
		}
	}
	return "", nil
}
