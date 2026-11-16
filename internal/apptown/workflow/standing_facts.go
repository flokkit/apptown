package workflow

import (
	"context"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
)

type standingIssuerFacts struct {
	facts   identity.StandingIssuerFacts
	allowed []error
	err     error
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
	procedures                                                                        map[string]standingProcedureFact
	issuer                                                                            standingIssuerFacts
}

func (f standingProjectFacts) level(in JanitorChoices) (string, error) {
	if f.choiceErr != nil {
		return "", f.choiceErr
	}
	return projectLevel(in, f.choice), nil
}

// standingFacts assembles read facts for one judgment set. Errors stay attached
// to their facts so the judgment preserves its refusal order.
func (s StandingRules) standingFacts(ctx context.Context, q database.Queryer, rule StandingRevision, set []ProjectWorkflows) []standingProjectFacts {
	out := make([]standingProjectFacts, len(set))
	for i, p := range set {
		f := &out[i]
		f.procedures = map[string]standingProcedureFact{}
		f.activeErr = project.RefuseUnlessActive(ctx, q, p.ProjectID)
		choices, err := workflowsql.New(q.SQL()).LatestStandingProjectChoice(ctx, p.ProjectID)
		f.choiceErr = err
		if err == nil {
			var choice *standingProjectAct
			choice, f.choiceErr = projectChoiceFrom(firstProjectChoice(choices))
			f.choice = choiceLevel(choice)
		}
		f.independent, f.independentErr = workflowsql.New(q.SQL()).StandingIndependentApproval(ctx, p.ProjectID)
		f.resource, f.resourceErr = project.AuthorityResourceIn(ctx, q, p.ProjectID)
		f.approver, f.approverErr = workflowsql.New(q.SQL()).ApproverEligibility(ctx, workflowsql.ApproverEligibilityParams{ProjectID: p.ProjectID})
		if s.deps.Portfolio != nil {
			f.reach, f.reachErr = s.deps.Portfolio.StandingReach(ctx, q, p.ProjectID)
			f.hold, f.holdErr = s.deps.Portfolio.StandingHold(ctx, q, p.ProjectID)
		}
		var procedures map[string]StandingProcedureResult
		err = access.ErrAuthorityUnavailable
		if s.deps.Procedures != nil {
			procedures, err = s.deps.Procedures.StandingProcedures(ctx, q, p.ProjectID, p.Workflows)
		}
		for _, name := range p.Workflows {
			result := procedures[name]
			if err != nil {
				result.Err = err
			}
			if !p.target.IsZero() && result.Procedure.Trigger == "publication" {
				reader, ok := s.deps.Procedures.(standingTargetProcedures)
				if !ok {
					result.Err = fault.New(fault.Conflict, "target procedure observation is unavailable")
				} else {
					result.Procedure, result.Err = reader.StandingTargetProcedure(ctx, q, p.ProjectID, p.target, name)
				}
			}
			f.procedures[name] = standingProcedureFact{result.Procedure, result.Err}
		}
	}
	var permissions []access.Permission
	for _, f := range out {
		for _, cap := range []access.Capability{access.CapChangeApprove, access.CapChangeLand} {
			permissions = append(permissions, access.Permission{Capability: cap, Resource: f.resource})
		}
	}
	var facts identity.StandingIssuerFacts
	err := access.ErrAuthorityUnavailable
	if s.deps.Identity != nil {
		facts, err = s.deps.Identity.StandingIssuerAllowsIn(ctx, q, rule.Issuer.Principal, permissions...)
	}
	for i := range out {
		out[i].issuer = standingIssuerFacts{facts: facts, err: err}
		if err == nil {
			out[i].issuer.allowed = facts.Allowed[2*i : 2*i+2]
		}
	}
	return out
}
