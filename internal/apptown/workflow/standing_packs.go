package workflow

import (
	"context"
	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/fault"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
)

// PackRuleIn selects current human authority for starting an assessment. Project
// eligibility and approval meanings are checked again before opening any Change.
func (s StandingRules) PackRuleIn(ctx context.Context, q database.Queryer) (StandingRevision, error) {
	rows, err := workflowsql.New(q.SQL()).CurrentStandingRules(ctx)
	if err != nil {
		return StandingRevision{}, err
	}
	for _, row := range rows {
		rule, err := readStandingRevision(row)
		if err != nil {
			return rule, err
		}
		if !rule.Enabled || !rule.Question.Definition.Categories.WaysOfWorking {
			continue
		}
		if _, err := normalizeStanding(rule.Question.Definition); err != nil {
			continue
		}
		if err := identity.RequireEnabledIn(ctx, q, access.Principal{Kind: "user", ID: rule.Issuer.Principal}); err != nil {
			if standingRefusal(err) {
				continue
			}
			return StandingRevision{}, err
		}
		return rule, nil
	}
	return StandingRevision{}, nil
}

// PackReviewAuthority rechecks the enabled revision before an assessment starts.
// The transaction is the publication's or Enable's transaction.
type packReviewAuthority struct {
	tx   *database.Tx
	rule StandingRevision
}

func (a packReviewAuthority) RequirePackReview(ctx context.Context) error {
	current, err := currentStanding(ctx, a.tx, a.rule.RuleID)
	if err != nil {
		return err
	}
	if !current.Enabled || current.Revision != a.rule.Revision || !current.Question.Definition.Categories.WaysOfWorking {
		return errStandingRevision
	}
	return identity.RequireEnabledIn(ctx, a.tx, access.Principal{Kind: "user", ID: current.Issuer.Principal})
}

// OpenPackUplift is the trusted what-if follow-up. Its rule and project are
// checked under the Change admission locks; publication alone grants nothing.
func (s *Service) OpenPackUplift(ctx context.Context, in Uplift) (*Change, error) {
	caller, ok := access.CallerFrom(ctx)
	if !ok || !caller.Verified || caller.Principal.Instrument != access.SystemInstrument || caller.Principal.Via != "schedule" || caller.Principal.ProjectID != in.ProjectID || s.standing == nil || in.RuleID.IsZero() {
		return nil, fault.New(fault.Forbidden, "pack maintenance needs current standing authority")
	}
	in.validate = func(ctx context.Context, tx *database.Tx, _ *Change) error {
		if _, err := workflowsql.New(tx.SQL()).LockStandingRule(ctx, in.RuleID); err != nil {
			return err
		}
		rule, err := currentStanding(ctx, tx, in.RuleID)
		if err != nil {
			return err
		}
		if !rule.Question.Definition.Categories.WaysOfWorking || rule.Revision != in.RuleRevision {
			return errStandingRevision
		}
		if _, err := s.standing.eligibleForIn(ctx, tx, rule, in.ProjectID, in.Workflow); err != nil {
			return err
		}
		state, err := queueStateIn(ctx, tx, in.ProjectID, in.Queue)
		if err != nil {
			return err
		}
		if state.Stopped() {
			return fault.New(fault.Conflict, "pack updates stopped after three failed tries")
		}
		return nil
	}
	return (changeStore{s.db, s.clocked}).openUplift(ctx, in, "apptown-system")
}

// CheckAdoptionTarget verifies the exact target before the fix and classifier.
// It never judges application files: the required checks judge repairs.
func (e *Execution) CheckAdoptionTarget() (adoption.RoutineTarget, error) {
	if e.service.standing == nil {
		return adoption.RoutineTarget{}, fault.New(fault.Unavailable, "pack maintenance authority is unavailable")
	}
	rules := e.service.standing
	rule, _, _, err := rules.standingForWorkflow(e.ctx, e.run.ProjectID, e.run.WorkflowName, e.run.AdoptionID)
	if err != nil {
		return adoption.RoutineTarget{}, err
	}
	if rule.RuleID.IsZero() || !rule.Question.Definition.Categories.WaysOfWorking {
		return adoption.RoutineTarget{}, fault.New(fault.Conflict, "no eligible keep packs current rule")
	}
	if _, err := rules.eligibleForIn(e.ctx, e.service.db, rule, e.run.ProjectID, e.run.WorkflowName, e.run.AdoptionID); err != nil {
		return adoption.RoutineTarget{}, err
	}
	ch, err := readChangeInProject(e.ctx, e.service.db, e.run.ChangeID, e.run.ProjectID)
	if err != nil {
		return adoption.RoutineTarget{}, err
	}
	target, err := rules.assessmentAdoption(e.ctx, standingObservation{change: *ch}, rule.Question.Definition.Categories)
	if err != nil {
		return adoption.RoutineTarget{}, err
	}
	if target == nil {
		return adoption.RoutineTarget{}, fault.New(fault.Conflict, "this change has no assessed adoption target")
	}
	return *target, nil
}

// PackReviewAuthorityIn binds assessment admission to the consent transaction.
func (s StandingRules) PackReviewAuthorityIn(tx *database.Tx, rule StandingRevision) packReviewAuthority {
	return packReviewAuthority{tx: tx, rule: rule}
}
