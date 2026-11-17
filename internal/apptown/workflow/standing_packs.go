package workflow

import (
	"context"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/fault"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
)

// PackRuleIn selects current issuer authority for starting an assessment. Project
// eligibility and approval meanings are checked again before opening any Change.
func (s StandingRules) PackRuleIn(ctx context.Context, q database.Queryer) (StandingRevision, error) {
	rule, err := latestStanding(ctx, q)
	if err != nil {
		return StandingRevision{}, err
	}
	if rule == nil || !rule.Enabled || !rule.Choices.Packs {
		return StandingRevision{}, nil
	}
	if _, err := s.standingIssuerIn(ctx, q, *rule); err != nil {
		if standingRefusal(err) {
			return StandingRevision{}, nil
		}
		return StandingRevision{}, err
	}
	return *rule, nil
}

// PackReviewAuthority rechecks the enabled revision before an assessment starts.
// The transaction is the publication's or Enable's transaction.
type packReviewAuthority struct {
	rules StandingRules
	tx    *database.Tx
	rule  StandingRevision
}

func (a packReviewAuthority) RequirePackReview(ctx context.Context) error {
	current, err := currentStanding(ctx, a.tx, a.rule.RuleID)
	if err != nil {
		return err
	}
	if !current.Enabled || current.Revision != a.rule.Revision || !current.Choices.Packs {
		return errStandingRevision
	}
	_, err = a.rules.standingIssuerIn(ctx, a.tx, current)
	return err
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
		if !rule.Choices.Packs || rule.Revision != in.RuleRevision {
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

	queue, err := e.IsQueue()
	if err != nil {
		return adoption.RoutineTarget{}, err
	}
	if !queue {
		ch, err := readChangeInProject(e.ctx, e.service.db, e.run.ChangeID, e.run.ProjectID)
		if err != nil {
			return adoption.RoutineTarget{}, err
		}
		return database.Transact(e.ctx, e.service.db, func(tx *database.Tx) (adoption.RoutineTarget, error) {
			return e.service.standing.deps.Adoptions.CheckRoutineTargetIn(e.ctx, tx, ch.ProjectID, ch.ID, ch.TargetAdoptionID, ch.ExpectedRevision, []byte(ch.TargetCandidateJSON))
		})
	}
	rules := e.service.standing
	rule, _, _, err := rules.standingForWorkflow(e.ctx, e.run.ProjectID, e.run.WorkflowName, e.run.AdoptionID)
	if err != nil {
		return adoption.RoutineTarget{}, err
	}
	if rule.RuleID.IsZero() || !rule.Choices.Packs {
		return adoption.RoutineTarget{}, fault.New(fault.Conflict, "no eligible keep packs current rule")
	}
	if _, err := rules.eligibleForIn(e.ctx, e.service.db, rule, e.run.ProjectID, e.run.WorkflowName, e.run.AdoptionID); err != nil {
		return adoption.RoutineTarget{}, err
	}
	ch, err := readChangeInProject(e.ctx, e.service.db, e.run.ChangeID, e.run.ProjectID)
	if err != nil {
		return adoption.RoutineTarget{}, err
	}
	target, err := rules.assessmentAdoption(e.ctx, standingObservation{change: *ch}, rule.Choices)
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
	return packReviewAuthority{rules: s, tx: tx, rule: rule}
}
