package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/jobs"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

type standingAcceptanceRequests interface {
	EnqueueStandingAcceptanceIn(context.Context, *database.Tx, string) error
}

type standingDecision struct {
	StepID        ids.ID              `json:"stepId"`
	RetryRevision int64               `json:"retryRevision"`
	RuleID        ids.ID              `json:"ruleId"`
	RuleRevision  int64               `json:"ruleRevision"`
	Reason        string              `json:"reason,omitempty"`
	Assessment    *StandingAssessment `json:"assessment,omitempty"`
}

func (s *Service) acceptStanding(ctx context.Context, run *Run) (bool, error) {
	if s.standing == nil || s.standingRequests == nil || run == nil || run.Status != RunRunning || !run.ParentRunID.IsZero() || run.ChangeID.IsZero() {
		return false, nil
	}
	authority, err := rootAuthorityIn(ctx, s.db, run.ID)
	if err != nil {
		return false, err
	}
	if eval, err := evalAuthorityHolds(ctx, s.db, authority, run.ProjectID); err != nil || eval {
		return false, err
	}
	steps, err := readSteps(ctx, s.db, run.ID)
	if err != nil {
		return false, err
	}
	if !settled(steps, run.CurrentStage) {
		return false, nil
	}
	for _, step := range steps {
		if step.Stage != run.CurrentStage || step.Status != StatusPending || !isAcceptance(run, step) {
			continue
		}
		cfg, err := ReadApprovalConfig(step.Config)
		if err != nil {
			return false, err
		}
		if cfg.Standing == nil {
			continue
		}
		return false, s.db.InTx(ctx, func(tx *database.Tx) error {
			current, err := lockCurrentRunIn(ctx, tx, run)
			if err != nil {
				return err
			}
			done, err := standingConsidered(ctx, tx, current.ID, step.ID, current.RetryRevision)
			if err != nil || done {
				return err
			}
			return s.standingRequests.EnqueueStandingAcceptanceIn(ctx, tx, fmt.Sprintf("%s/%s/%d", run.ID, step.ID, run.RetryRevision))
		})
	}
	return false, nil
}

// RunStandingAcceptance is trusted queue work. Its key binds the exact decision;
// authority comes from current rule records, never from job arguments.
func (s *Service) RunStandingAcceptance(ctx context.Context, key string, _ map[string]any) (map[string]any, error) {
	parts := strings.Split(key, "/")
	if len(parts) != 3 {
		return nil, jobs.Permanent(errors.New("invalid standing acceptance key"))
	}
	runID, err := ids.Parse(parts[0])
	if err != nil {
		return nil, jobs.Permanent(err)
	}
	stepID, err := ids.Parse(parts[1])
	if err != nil {
		return nil, jobs.Permanent(err)
	}
	revision, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return nil, jobs.Permanent(err)
	}
	cast, err := s.processStanding(ctx, runID, stepID, revision)
	return map[string]any{"cast": cast}, err
}

func (s *Service) processStanding(ctx context.Context, runID, stepID ids.ID, revision int64) (bool, error) {
	if s.standing == nil {
		return false, errors.New("standing approval processing is unavailable")
	}
	run, err := readRun(ctx, s.db, runID)
	if errors.Is(err, ErrRunMoved) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	step, err := readStep(ctx, s.db, stepID)
	if err != nil {
		return false, err
	}
	if run.Status != RunRunning || run.RetryRevision != revision || step.RunID != run.ID || step.Status != StatusPending || step.Stage != run.CurrentStage || !isAcceptance(run, step) {
		return false, nil
	}
	considered, err := standingConsidered(ctx, s.db, runID, stepID, revision)
	if err != nil || considered {
		return false, err
	}
	cfg, err := ReadApprovalConfig(step.Config)
	if err != nil {
		return false, err
	}
	if cfg.Standing == nil {
		return false, nil
	}
	rule, _, _, err := s.standing.standingForWorkflow(ctx, run.ProjectID, run.WorkflowName, run.AdoptionID)
	if err != nil {
		return false, err
	}
	decision := standingDecision{StepID: stepID, RetryRevision: revision, RuleID: rule.RuleID, RuleRevision: rule.Revision}
	if rule.RuleID.IsZero() {
		decision.Reason = "no eligible standing approval covers this project and workflow"
	}
	var assessed StandingAssessment
	if decision.Reason == "" {
		assessed, err = s.standing.assess(ctx, rule.RuleID, runID)
		if err != nil {
			if !standingRefusal(err) {
				return false, err
			}
			decision.Reason = err.Error()
		} else if assessed.Inspection.Classification != "routine-dependency" && assessed.Inspection.Classification != "routine-adoption" {
			decision.Reason = assessed.Inspection.Reason
		}
		if err == nil && assessed.Inspection.Classification != "routine-dependency" && assessed.Inspection.Classification != "routine-adoption" && decision.Reason == "" {
			decision.Reason = "the candidate could not be classified within the rule"
		}
	}
	if s.abandonments.locks == nil {
		return false, errors.New("repository locking is unavailable")
	}
	release, err := s.abandonments.locks.Acquire(ctx, run.ProjectID)
	if err != nil {
		return false, err
	}
	defer release()
	tip, err := s.pins.acceptedTip(ctx, RunRef{ID: run.ID, ProjectID: run.ProjectID})
	if err != nil {
		return false, err
	}
	adds, err := s.adds.forStep(ctx, run, step)
	if err != nil {
		return false, err
	}
	if decision.Reason == "" && (tip == "" || assessed.Inspection.Candidate != tip || assessed.RetryRevision != revision) {
		decision.Reason = "the candidate or retry revision changed during assessment"
	}
	if decision.Reason == "" {
		decision.Assessment = &assessed
	}
	raw, err := json.Marshal(assessed)
	if err != nil {
		return false, err
	}
	digest := sha256.Sum256(raw)
	var result reworkedResult
	cast, err := database.Transact(ctx, s.db, func(tx *database.Tx) (bool, error) {
		current, err := lockCurrentRunIn(ctx, tx, run)
		if err != nil {
			return false, err
		}
		st, err := stepResult(workflowsql.New(tx.SQL()).StepForUpdate(ctx, workflowsql.StepForUpdateParams{ID: stepID}))
		if err != nil {
			return false, err
		}
		if current.RetryRevision != revision || st.Status != StatusPending || st.Stage != current.CurrentStage {
			return false, nil
		}
		done, err := standingConsidered(ctx, tx, runID, stepID, revision)
		if err != nil || done {
			return false, err
		}
		if decision.Reason == "" {
			if _, err = workflowsql.New(tx.SQL()).LockStandingRule(ctx, rule.RuleID); err != nil {
				return false, err
			}
			fresh, err := currentStanding(ctx, tx, rule.RuleID)
			if err != nil {
				return false, err
			}
			proc, e := s.standing.eligibleForIn(ctx, tx, fresh, current.ProjectID, current.WorkflowName, current.AdoptionID)
			if e != nil {
				if !standingRefusal(e) {
					return false, e
				}
				decision.Reason = e.Error()
			} else if fresh.Revision != rule.Revision || proc.Acceptance.Means != cfg.Standing.Means {
				decision.Reason = "the standing approval changed during assessment"
			}
			if decision.Reason == "" {
				level, err := s.standing.effectiveLevelIn(ctx, tx, fresh.Question.Definition, current.ProjectID)
				if err != nil {
					return false, err
				}
				if level != assessed.Level {
					decision.Reason = "the project update level changed during assessment"
				}
			}
			if decision.Reason == "" && assessed.Adoption != nil {
				if _, err := adoption.CurrentForUpdateIn(ctx, tx, current.ProjectID); err != nil {
					return false, err
				}
				ch, err := readChangeInProject(ctx, tx, current.ChangeID, current.ProjectID)
				if err != nil {
					return false, err
				}
				target, err := s.standing.deps.Adoptions.CheckRoutineTargetIn(ctx, tx, ch.ProjectID, ch.ID, ch.TargetAdoptionID, ch.ExpectedRevision, []byte(ch.TargetCandidateJSON))
				if err != nil {
					if !standingRefusal(err) {
						return false, err
					}
					decision.Reason = err.Error()
				} else if target != *assessed.Adoption {
					decision.Reason = "the assessed adoption changed before acceptance"
				}
			}
			if decision.Reason == "" {
				why, err := standingEvidence(ctx, tx, current, false, 0)
				if err != nil {
					return false, err
				}
				decision.Reason = why
			}
		}
		if decision.Reason != "" {
			return false, recordReceiptIn(ctx, tx, runID, "acceptance.standing.withheld", decision, s.now())
		}
		proc, err := s.standing.eligibleForIn(ctx, tx, rule, current.ProjectID, current.WorkflowName, current.AdoptionID)
		if err != nil {
			return false, err
		}
		authority := &standingVote{Level: assessed.Level, RuleID: rule.RuleID, Revision: rule.Revision, Issuer: rule.Issuer, Means: cfg.Standing.Means, AssessmentDigest: hex.EncodeToString(digest[:])}
		result, err = (reworkStore{s.db, s.clocked}).approveIn(ctx, tx, current, st, ApprovalVote{Approver: "the janitor", Principal: rule.Issuer.Principal, Via: "standing", Decision: VoteApprove, Standing: authority, Comment: standingAttribution(authority), Candidate: tip, Adds: adds, AlsoApproves: []string{proc.Landing.Means}})
		if err != nil {
			return false, err
		}
		return true, recordReceiptIn(ctx, tx, runID, "acceptance.standing.applied", decision, s.now())
	})
	if errors.Is(err, ErrRunMoved) || fault.KindOf(err) == fault.NotFound {
		return false, nil
	}
	if err == nil && cast {
		s.dispatchRework(result)
		// The vote is cast after the advancer moved on, so nothing else
		// advances the run: as after a person's vote (Approve).
		s.advanceCommitted(RunRef{ID: run.ID, ProjectID: run.ProjectID})
	}
	return cast, err
}
