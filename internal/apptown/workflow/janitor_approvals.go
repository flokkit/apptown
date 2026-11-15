package workflow

import (
	"context"
	"fmt"

	"github.com/flokkit/apptown/internal/ids"
)

// ChangedMeaningSentence names the consent action shared by Janitor and Attention.
func (r StandingRevision) ChangedMeaningSentence() string {
	name := "Dependency updates"
	if r.Question.Definition.Categories.WaysOfWorking {
		name = "Keep packs current"
	}
	return name + " approval changed meaning; enable it again in Settings"
}

// janitorEligibility uses the same standing workflows as schedule admission.
func (s *Service) janitorEligibility(ctx context.Context, projects []JanitorProject, rules []StandingRevision) (map[ids.ID]map[string]StandingEligibilityResult, error) {
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
				for _, rule := range rules {
					if pinsWorkflow(rule, name) {
						result.Unassessed = append(result.Unassessed, rule.RuleID)
					}
				}
				results[name] = result
			}
			all[request.ProjectID] = results
		}
	}
	return all, err
}

// ChangedStandingApprovals reads current eligibility across caller-visible projects.
func (s *Service) ChangedStandingApprovals(ctx context.Context) ([]StandingRevision, error) {
	rules, err := s.standing.janitorRules(ctx)
	if err != nil {
		return nil, err
	}
	visible, err := s.authority.VisibleProjects(ctx, actionReadProject)
	if err != nil {
		return nil, err
	}
	projects := make([]JanitorProject, 0, len(visible))
	for _, pid := range visible {
		facts, err := s.procedureFacts(ctx, pid)
		if err != nil {
			return nil, err
		}
		projects = append(projects, JanitorProject{ProjectID: pid, Facts: facts})
	}
	eligibility, err := s.janitorEligibility(ctx, projects, rules)
	if err != nil {
		return nil, err
	}
	changed := map[ids.ID]bool{}
	for _, results := range eligibility {
		for _, result := range results {
			if result.Err != nil {
				return nil, result.Err
			}
			if len(result.Unassessed) > 0 {
				return nil, fmt.Errorf("standing approval eligibility could not be assessed for rules %v", result.Unassessed)
			}
			for _, id := range result.MeaningChanged {
				changed[id] = true
			}
		}
	}
	out := []StandingRevision{}
	for _, rule := range rules {
		if changed[rule.RuleID] {
			out = append(out, rule)
		}
	}
	return out, nil
}
