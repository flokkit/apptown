package workflow

import (
	"context"
	"encoding/json"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

type standingIssuerFacts struct {
	facts   identity.StandingIssuerFacts
	allowed []error
	err     error
}
type standingProcedureKey struct {
	rule     ids.ID
	workflow string
}
type standingProcedureFact struct {
	procedure StandingProcedure
	err       error
}
type standingProjectFacts struct {
	activeErr, choiceErr, independentErr, resourceErr, approverErr, reachErr, holdErr error
	choice                                                                            string
	independent                                                                       bool
	resource                                                                          access.Resource
	approver                                                                          workflowsql.ApproverEligibilityRow
	reach                                                                             StandingReach
	hold                                                                              string
	procedures                                                                        map[standingProcedureKey]standingProcedureFact
	issuers                                                                           map[ids.ID]standingIssuerFacts
}

func (f standingProjectFacts) level(in StandingDefinition) (string, error) {
	if f.choiceErr != nil {
		return "", f.choiceErr
	}
	if f.choice != "" {
		return f.choice, nil
	}
	return in.Categories.Dependencies, nil
}

// standingFacts assembles read facts for one judgment set. Errors stay attached
// to their facts so the judgment preserves its refusal order.
func (s StandingRules) standingFacts(ctx context.Context, q database.Queryer, rules []StandingRevision, set []ProjectWorkflows) []standingProjectFacts {
	out := make([]standingProjectFacts, len(set))
	for i, p := range set {
		f := &out[i]
		f.procedures = map[standingProcedureKey]standingProcedureFact{}
		f.issuers = map[ids.ID]standingIssuerFacts{}
		relevant := false
		for _, r := range rules {
			for _, name := range p.Workflows {
				relevant = relevant || pinsWorkflow(r, name)
			}
		}
		if !relevant {
			continue
		}
		f.activeErr = project.RefuseUnlessActive(ctx, q, p.ProjectID)
		choices, err := workflowsql.New(q.SQL()).LatestStandingProjectChoice(ctx, p.ProjectID)
		f.choiceErr = err
		if err == nil && len(choices) > 0 {
			var act standingProjectAct
			f.choiceErr = json.Unmarshal(choices[0], &act)
			if f.choiceErr == nil {
				switch act.Level {
				case "off", "routine", "aggressive":
					f.choice = act.Level
				default:
					f.choiceErr = fault.New(fault.Conflict, "project janitor level is invalid")
				}
			}
		}
		f.independent, f.independentErr = workflowsql.New(q.SQL()).StandingIndependentApproval(ctx, p.ProjectID)
		f.resource, f.resourceErr = project.AuthorityResourceIn(ctx, q, p.ProjectID)
		f.approver, f.approverErr = workflowsql.New(q.SQL()).ApproverEligibility(ctx, workflowsql.ApproverEligibilityParams{ProjectID: p.ProjectID})
		if s.deps.Portfolio != nil {
			f.reach, f.reachErr = s.deps.Portfolio.StandingReach(ctx, q, p.ProjectID)
			f.hold, f.holdErr = s.deps.Portfolio.StandingHold(ctx, q, p.ProjectID)
		}
		names := []string{}
		for _, name := range p.Workflows {
			for _, rule := range rules {
				if pinsWorkflow(rule, name) && !(rule.Question.Definition.Categories.WaysOfWorking && rule.Question.Definition.Scope == "instance") {
					names = append(names, name)
					break
				}
			}
		}
		if len(names) > 0 {
			var procedures map[string]StandingProcedureResult
			err := access.ErrAuthorityUnavailable
			if s.deps.Procedures != nil {
				procedures, err = s.deps.Procedures.StandingProcedures(ctx, q, p.ProjectID, names)
			}
			for _, name := range names {
				result := procedures[name]
				if err != nil {
					result.Err = err
				}
				for _, rule := range rules {
					if !(rule.Question.Definition.Categories.WaysOfWorking && rule.Question.Definition.Scope == "instance") {
						f.procedures[standingProcedureKey{rule.RuleID, name}] = standingProcedureFact{result.Procedure, result.Err}
					}
				}
			}
		}
		for _, name := range p.Workflows {
			for _, rule := range rules {
				if pinsWorkflow(rule, name) && rule.Question.Definition.Categories.WaysOfWorking && rule.Question.Definition.Scope == "instance" {
					proc, err := s.readInstanceStandingProcedure(ctx, q, p, name)
					f.procedures[standingProcedureKey{rule.RuleID, name}] = standingProcedureFact{proc, err}
				}
			}
		}

	}
	// Each issuer operation evaluates all project resources from the same identity
	// facts; the returned decisions remain distinct for approve and land.
	issuers := map[ids.ID]bool{}
	for _, r := range rules {
		issuers[r.Issuer.Principal] = true
	}
	for issuer := range issuers {
		var permissions []access.Permission
		var projects []int
		for i, p := range set {
			relevant := false
			for _, r := range rules {
				if r.Issuer.Principal == issuer {
					for _, name := range p.Workflows {
						relevant = relevant || pinsWorkflow(r, name)
					}
				}
			}
			if !relevant {
				continue
			}
			projects = append(projects, i)
			for _, cap := range []access.Capability{access.CapChangeApprove, access.CapChangeLand} {
				permissions = append(permissions, access.Permission{Capability: cap, Resource: out[i].resource})
			}
		}
		if len(projects) == 0 {
			continue
		}
		var facts identity.StandingIssuerFacts
		err := access.ErrAuthorityUnavailable
		if s.deps.Identity != nil {
			facts, err = s.deps.Identity.StandingIssuerAllowsIn(ctx, q, issuer, permissions...)
		}
		for j, i := range projects {
			f := standingIssuerFacts{facts: facts, err: err}
			if err == nil {
				f.allowed = facts.Allowed[2*j : 2*j+2]
			}
			out[i].issuers[issuer] = f
		}
	}
	return out
}

func (s StandingRules) readInstanceStandingProcedure(ctx context.Context, q database.Queryer, p ProjectWorkflows, name string) (StandingProcedure, error) {
	if s.deps.Procedures == nil {
		return StandingProcedure{}, access.ErrAuthorityUnavailable
	}
	if !p.target.IsZero() {
		reader, ok := s.deps.Procedures.(standingTargetProcedures)
		if !ok {
			return StandingProcedure{}, fault.New(fault.Conflict, "target procedure observation is unavailable")
		}
		return reader.StandingTargetProcedure(ctx, q, p.ProjectID, p.target, name)
	}
	reader, ok := s.deps.Procedures.(standingInstanceProcedures)
	if !ok || s.deps.Publications == nil {
		return StandingProcedure{}, fault.New(fault.Conflict, "published procedure observation is unavailable")
	}
	library, err := s.deps.Publications.PublishedWorkflows(ctx, q)
	if err != nil {
		return StandingProcedure{}, err
	}
	return reader.InstanceStandingProcedure(name, library)
}
