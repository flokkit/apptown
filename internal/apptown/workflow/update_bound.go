package workflow

import (
	"context"

	"github.com/flokkit/apptown/internal/ids"
)

// UpdateChoicesForRun is classification input for an admitted run, never assent.
// Eligibility is read afresh from the rule owner; the bound is never assent.
func (s StandingRules) UpdateChoicesForRun(ctx context.Context, runID ids.ID) (JanitorChoices, error) {
	run, err := readRun(ctx, s.db, runID)
	if err != nil {
		return JanitorChoices{}, err
	}
	source, err := ReadSourceIn(ctx, s.db, RunRef{ID: runID, ProjectID: run.ProjectID})
	if err != nil {
		return JanitorChoices{}, err
	}
	root, err := readRun(ctx, s.db, source.RootRunID)
	if err != nil {
		return JanitorChoices{}, err
	}
	r, err := latestStanding(ctx, s.db)
	if err != nil {
		return JanitorChoices{}, err
	}
	if r == nil {
		return JanitorChoices{}, nil
	}
	if _, err := s.eligibleForIn(ctx, s.db, *r, run.ProjectID, root.WorkflowName); err != nil {
		if standingRefusal(err) {
			return JanitorChoices{}, nil
		}
		return JanitorChoices{}, err
	}
	c := r.Choices
	c.Dependencies, err = s.effectiveLevelIn(ctx, s.db, c, run.ProjectID)
	return c, err
}
