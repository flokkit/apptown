package workflow

import (
	"context"
	"github.com/flokkit/apptown/internal/ids"
)

func standingEligibilityOne(s interface {
	StandingEligibility(context.Context, []ProjectWorkflows) (map[ids.ID]map[string]StandingEligibilityResult, error)
}, ctx context.Context, pid ids.ID, name string) (StandingRevision, string, string, error) {
	results, err := s.StandingEligibility(ctx, []ProjectWorkflows{{ProjectID: pid, Workflows: []string{name}}})
	if err != nil {
		return StandingRevision{}, "", "", err
	}
	r := results[pid][name]
	return r.Rule, r.Level, r.Refused, r.Err
}
