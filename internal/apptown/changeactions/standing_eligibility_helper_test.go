package changeactions_test

import (
	"context"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/ids"
)

func standingEligibilityOne(s interface {
	StandingEligibility(context.Context, []workflow.ProjectWorkflows) (map[ids.ID]map[string]workflow.StandingEligibilityResult, error)
}, ctx context.Context, pid ids.ID, name string) (workflow.StandingRevision, string, string, error) {
	results, err := s.StandingEligibility(ctx, []workflow.ProjectWorkflows{{ProjectID: pid, Workflows: []string{name}}})
	if err != nil {
		return workflow.StandingRevision{}, "", "", err
	}
	r := results[pid][name]
	return r.Rule, r.Level, r.Refused, r.Err
}
