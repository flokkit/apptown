package workflow

import (
	"context"

	"github.com/flokkit/apptown/internal/ids"
)

func (s *Service) janitorEligibility(ctx context.Context, projects []JanitorProject, rule *StandingRevision) (map[ids.ID]map[string]StandingEligibilityResult, error) {
	set := make([]ProjectWorkflows, 0, len(projects))
	for _, p := range projects {
		request := ProjectWorkflows{ProjectID: p.ProjectID}
		for _, d := range parseProcedures(p.Facts) {
			if d.doc != nil {
				if _, ok := d.doc.Standing(); ok {
					request.Workflows = append(request.Workflows, d.name)
				}
			}
		}
		set = append(set, request)
	}
	all, err := s.standing.StandingEligibility(ctx, set)
	if err != nil {
		all = map[ids.ID]map[string]StandingEligibilityResult{}
		for _, request := range set {
			results := map[string]StandingEligibilityResult{}
			for _, name := range request.Workflows {
				result := StandingEligibilityResult{Err: err}
				if rule != nil {
					result.Unassessed = append(result.Unassessed, rule.RuleID)
				}
				results[name] = result
			}
			all[request.ProjectID] = results
		}
	}
	return all, err
}
