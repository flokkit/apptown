package workflow

import (
	"context"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/fault"
)

var errNotApproval = fault.New(fault.Invalid, "this step is not an approval")

// StepApprovers is who could decide an approval step now, for a person
// stopped at it: the step's approver expressions (or the project's
// defaults, as eligibility reads them) resolved to people. Opener names
// the change's opener when the project requires independent approval,
// who may not decide it; administrators may always decide.
type StepApprovers struct {
	Approvers []identity.Approver `json:"approvers"`
	Opener    string              `json:"opener,omitempty"`
}

// Approvers reads an approval step's approvers for a reader of its run.
func (s *Service) Approvers(ctx context.Context, stepID ids.ID) (StepApprovers, error) {
	observed, err := s.Step(ctx, stepID)
	if err != nil {
		return StepApprovers{}, err
	}
	if observed.Step.Type != "approval" {
		return StepApprovers{}, errNotApproval
	}
	cfg, err := ReadApprovalConfig(observed.Step.Config)
	if err != nil {
		return StepApprovers{}, err
	}
	policy, err := workflowsql.New(s.db.SQL()).ApproverEligibility(ctx, workflowsql.ApproverEligibilityParams{ProjectID: observed.Run.ProjectID, ChangeID: observed.Run.ChangeID})
	if err != nil {
		return StepApprovers{}, err
	}
	approvers, err := identity.ApproversIn(ctx, s.db, observed.Run.ProjectID, approvalExpressions(cfg.Approvers, policy.DefaultApprovers), s.now())
	if err != nil {
		return StepApprovers{}, err
	}
	return StepApprovers{Approvers: approvers, Opener: policy.Author}, nil
}
