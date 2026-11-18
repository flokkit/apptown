package workflow

import (
	"context"
	"crypto/sha256"
	"database/sql"
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
	"github.com/flokkit/apptown/internal/strs"
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
	again, err := landingReassessmentIn(ctx, s.db, run)
	if err != nil {
		return false, err
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
		if step.Stage != run.CurrentStage || step.Status != StatusPending || !standingAnswers(run, step, again) {
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
	again, err := landingReassessmentIn(ctx, s.db, run)
	if err != nil {
		return false, err
	}
	if run.Status != RunRunning || run.RetryRevision != revision || step.RunID != run.ID || step.Status != StatusPending || step.Stage != run.CurrentStage || !standingAnswers(run, step, again) {
		return false, nil
	}
	// The rule is judged for the procedure the change was worked under: a
	// landing's is its accepted working run's.
	proc, err := standingProcedureRunIn(ctx, s.db, run)
	if err != nil {
		return false, err
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
	rule, _, _, err := s.standing.standingForWorkflow(ctx, run.ProjectID, proc.WorkflowName, proc.AdoptionID)
	if err != nil {
		return false, err
	}
	decision := standingDecision{StepID: stepID, RetryRevision: revision, RuleID: rule.RuleID, RuleRevision: rule.Revision}
	if rule.RuleID.IsZero() {
		decision.Reason = "no eligible standing approval covers this project and workflow"
	}
	if decision.Reason == "" && again != nil {
		// Only main merged into the accepted commit is judged again; work
		// published after the acceptance waits for a person.
		if decision.Reason, err = s.pins.cleanCatchUp(ctx, run, again.accepted.Source, again.candidate); err != nil {
			return false, err
		}
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
	var tip string
	var adds *ChangeAdds
	if again != nil {
		// A landing judges its pinned candidate; carry applies the
		// acceptance, with what it adds, once the rule has judged it.
		tip = again.candidate.Candidate
	} else {
		if tip, err = s.pins.acceptedTip(ctx, RunRef{ID: run.ID, ProjectID: run.ProjectID}); err != nil {
			return false, err
		}
		if adds, err = s.adds.forStep(ctx, run, step); err != nil {
			return false, err
		}
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
	decided := false
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
			_, e := s.standing.eligibleForIn(ctx, tx, fresh, current.ProjectID, proc.WorkflowName, proc.AdoptionID)
			if e != nil {
				if !standingRefusal(e) {
					return false, e
				}
				decision.Reason = e.Error()
			} else if fresh.Revision != rule.Revision {
				decision.Reason = "the standing approval changed during assessment"
			}
			if decision.Reason == "" {
				level, err := s.standing.effectiveLevelIn(ctx, tx, fresh.Choices, current.ProjectID)
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
		decided = true
		if decision.Reason != "" {
			return false, recordReceiptIn(ctx, tx, runID, "acceptance.standing.withheld", decision, s.now())
		}
		if again != nil {
			// The landing's own approvals take the acceptance through carry,
			// which reads this decision.
			return false, recordReceiptIn(ctx, tx, runID, "acceptance.standing.applied", decision, s.now())
		}
		procedure, err := s.standing.eligibleForIn(ctx, tx, rule, current.ProjectID, proc.WorkflowName, proc.AdoptionID)
		if err != nil {
			return false, err
		}
		authority := &standingVote{Level: assessed.Level, RuleID: rule.RuleID, Revision: rule.Revision, Issuer: rule.Issuer, Means: cfg.Standing.Means, AssessmentDigest: hex.EncodeToString(digest[:])}
		result, err = (reworkStore{s.db, s.clocked}).approveIn(ctx, tx, current, st, ApprovalVote{Approver: "the janitor", Principal: rule.Issuer.Principal, Via: "standing", Decision: VoteApprove, Standing: authority, Comment: standingAttribution(authority), Candidate: tip, Adds: adds, AlsoApproves: []string{procedure.Landing.Means}})
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
	}
	if err == nil && (cast || decided && again != nil) {
		// The decision is made after the advancer moved on, so nothing else
		// advances the run: as after a person's vote (Approve). A landing's
		// advance lets carry apply or withhold the acceptance.
		s.advanceCommitted(RunRef{ID: run.ID, ProjectID: run.ProjectID})
	}
	return cast, err
}

// landingReassessment is a landing whose carried acceptance is a standing
// vote on another commit than the landing's pinned candidate: catching up
// with main made a new candidate, and the rule judges it again.
type landingReassessment struct {
	accepted  acceptance
	candidate LandingCandidate
}

// catchUpReassessable reports whether accepted, carried to a landing pinned
// at c, is a standing vote on another commit. A person's yes was given on
// one commit and carries only to it (carryRefusalIn); a standing vote's
// authority is a rule, which is asked again about the candidate.
func catchUpReassessable(accepted acceptance, c LandingCandidate, pinned bool) bool {
	return accepted.Standing != nil && pinned && !c.Swept && accepted.Source != "" && c.Candidate != accepted.Source
}

// landingReassessmentIn is run's reassessment when run is such a landing,
// and nil otherwise.
func landingReassessmentIn(ctx context.Context, q database.Queryer, run *Run) (*landingReassessment, error) {
	if run == nil || !run.ParentRunID.IsZero() || run.ChangeID.IsZero() {
		return nil, nil
	}
	spec, err := run.Definition()
	if err != nil || spec.Role != "landing" {
		return nil, err
	}
	workID, err := workflowsql.New(q.SQL()).LandingAcceptedWork(ctx, run.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	accepted, err := acceptanceIn(ctx, q, workID)
	if err != nil {
		return nil, err
	}
	c, pinned, err := LandingCandidateIn(ctx, q, run.ID)
	if errors.Is(err, ErrLandingUnpinned) {
		return nil, nil
	}
	if err != nil || !catchUpReassessable(accepted, c, pinned) {
		return nil, err
	}
	return &landingReassessment{accepted: accepted, candidate: c}, nil
}

// standingAnswers reports whether the standing rule decides step: a working
// run's acceptance, or an approval of a landing it judges again.
func standingAnswers(run *Run, step *Step, again *landingReassessment) bool {
	return isAcceptance(run, step) || again != nil && step.Type == "approval"
}

// standingProcedureRunIn is the run whose procedure a standing rule covers
// for run: run itself, or a landing's accepted working run.
func standingProcedureRunIn(ctx context.Context, q database.Queryer, run *Run) (*Run, error) {
	spec, err := run.Definition()
	if err != nil || spec.Role != "landing" {
		return run, err
	}
	workID, err := workflowsql.New(q.SQL()).LandingAcceptedWork(ctx, run.ID)
	if err != nil {
		return nil, err
	}
	return readRun(ctx, q, workID)
}

// landingStandingDecisionIn is the standing rule's decision on landing's
// candidate for step at the landing's retry revision: the refusal carry
// records, "" when the rule held for that candidate, and decided false
// while the rule has not judged it yet.
func landingStandingDecisionIn(ctx context.Context, q database.Queryer, landing *Run, step *Step, candidate string) (string, bool, error) {
	for _, kind := range []string{"acceptance.standing.applied", "acceptance.standing.withheld"} {
		row, err := workflowsql.New(q.SQL()).LatestRunEventOfKind(ctx, workflowsql.LatestRunEventOfKindParams{RunID: landing.ID, Kind: kind})
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return "", false, err
		}
		var decision standingDecision
		if err := json.Unmarshal(row.PayloadJson, &decision); err != nil {
			return "", false, fmt.Errorf("run %d has an unreadable standing decision: %w", landing.ID, err)
		}
		if decision.StepID != step.ID || decision.RetryRevision != landing.RetryRevision {
			continue
		}
		short := strs.ShortSHA(candidate)
		switch {
		case kind == "acceptance.standing.withheld":
			return fmt.Sprintf("the standing approval was assessed again on the landing's candidate %s and withheld: %s", short, decision.Reason), true, nil
		case decision.Assessment == nil || decision.Assessment.Inspection.Candidate != candidate:
			return fmt.Sprintf("the standing approval was assessed again on another commit than the landing's candidate %s", short), true, nil
		}
		return "", true, nil
	}
	return "", false, nil
}
