package workflow

import (
	"context"
	"database/sql"
	"errors"

	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
)

func approvedUnderStanding(step *Step) bool {
	if step.Type != "approval" || step.Status != StatusPass || step.OverrideBy != "" {
		return false
	}
	ev, err := ReadApprovalEvidence(step.Evidence)
	if err != nil {
		return false
	}
	for _, vote := range ev.Decisions {
		if vote.Decision == VoteApprove && vote.Standing != nil && !vote.Standing.RuleID.IsZero() && vote.Standing.Revision > 0 && vote.Standing.AssessmentDigest != "" {
			return true
		}
	}
	return false
}

func (s StandingRules) checkStandingLanding(ctx context.Context, q database.Queryer, run *Run, vote *standingVote) (string, error) {
	// Serialize current authority with Disable until the merge claim commits.
	if _, err := workflowsql.New(q.SQL()).LockStandingRule(ctx, vote.RuleID); err != nil {
		return "", err
	}
	rule, err := currentStanding(ctx, q, vote.RuleID)
	if err != nil {
		return "", err
	}
	if rule.Revision != vote.Revision || rule.Issuer != vote.Issuer {
		return "the standing approval changed since acceptance", nil
	}
	workID, err := workflowsql.New(q.SQL()).LandingAcceptedWork(ctx, run.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return "this landing has no accepted working run", nil
	}
	if err != nil {
		return "", err
	}
	work, err := readRun(ctx, q, workID)
	if err != nil {
		return "", err
	}
	if _, err = s.eligibleForIn(ctx, q, rule, run.ProjectID, work.WorkflowName); err != nil {
		if standingRefusal(err) {
			return err.Error(), nil
		}
		return "", err
	}
	level, err := s.effectiveLevelIn(ctx, q, rule.Question.Definition, run.ProjectID)
	if err != nil {
		return "", err
	}
	if level != vote.Level {
		return "the project update level changed since acceptance", nil
	}
	return "", nil
}

func standingMergeRefusalIn(ctx context.Context, q database.Queryer, run *Run, rules *StandingRules) (string, error) {
	for id, depth := run.ID, 0; !id.IsZero() && depth <= maxCallDepth+1; depth++ {
		steps, err := readSteps(ctx, q, id)
		if err != nil {
			return "", err
		}
		for _, step := range steps {
			if !approvedUnderStanding(step) {
				continue
			}
			if rules == nil {
				return "standing approval processing is unavailable", nil
			}
			root, err := readRun(ctx, q, id)
			if err != nil {
				return "", err
			}
			ev, err := ReadApprovalEvidence(step.Evidence)
			if err != nil {
				return "", err
			}
			for _, vote := range ev.Decisions {
				if vote.Decision == VoteApprove && vote.Standing != nil {
					why, err := rules.checkStandingLanding(ctx, q, root, vote.Standing)
					if err != nil || why != "" {
						return why, err
					}
					if why, err := standingEvidence(ctx, q, root, true, 0); err != nil || why != "" {
						return why, err
					}
				}
			}
		}
		facts, err := workflowsql.New(q.SQL()).RunLandingAuthority(ctx, id)
		if err != nil {
			return "", err
		}
		id = facts.ParentRunID
	}
	return "", nil
}

// StandingLanding reads typed acceptance provenance for a completed landing's
// release follow-up. An approvers string is never a source of this fact.
func (s *Service) StandingLanding(ctx context.Context, ref RunRef) (bool, error) {
	if _, err := s.require(ctx, operationPermission{Action: actionReadRun, ProjectID: ref.ProjectID, RunID: ref.ID}); err != nil {
		return false, err
	}
	q := s.db
	run, err := readRun(ctx, q, ref.ID)
	if err != nil {
		return false, err
	}
	if run.ProjectID != ref.ProjectID {
		return false, ErrRunMoved
	}
	steps, err := readSteps(ctx, q, run.ID)
	if err != nil {
		return false, err
	}
	for _, step := range steps {
		if approvedUnderStanding(step) {
			return true, nil
		}
	}
	return false, nil
}
