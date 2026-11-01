package workflow

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
)

// An eval's authority: starting an eval
// is the one human act. Every run in one of its throwaway copies is admitted
// with that start recorded as its landing authority, "eval/<id> started by
// <person>", never a made-up approver. It counts only where the project of
// the effect, a merge or a deploy, is a copy of that same eval, as the copy
// record says; anywhere else it is no authority at all. Approval steps in
// such a run are settled by it and shown as eval-authorised: no person
// approved them, approvedByAPerson stays false, and they are never carried
// as acceptance to other work. In a checked landing a settled approval names
// the landing's candidate, as a person's vote does, so the merge's check
// that every approval assented to the commit it lands applies unchanged.

const evalAuthorityPrefix = "eval/"

// evalAuthorityOf is the landing authority an eval's runs carry.
func evalAuthorityOf(evalID ids.ID, startedBy string) string {
	return fmt.Sprintf("%s%d started by %s", evalAuthorityPrefix, evalID, startedBy)
}

// evalOfAuthority reads the eval an authority names; false when it is a
// person's landing authority, not an eval's.
func evalOfAuthority(authority string) (ids.ID, bool) {
	rest, ok := strings.CutPrefix(authority, evalAuthorityPrefix)
	if !ok {
		return ids.Nil, false
	}
	id, _, _ := strings.Cut(rest, " ")
	n, err := ids.Parse(id)
	return n, err == nil && !n.IsZero()
}

// evalCopyOfIn is the copy record's answer for a project: the eval it is a
// copy of, or 0.
func evalCopyOfIn(ctx context.Context, q database.Queryer, projectID ids.ID) (ids.ID, error) {
	id, err := workflowsql.New(q.SQL()).EvalCopyOf(ctx, projectID)
	found, err := database.Lookup(err)
	if err != nil || !found {
		return ids.Nil, err
	}
	return id, nil
}

// evalAuthorityHolds: authority is an eval's, and projectID is a copy of
// that eval. A person's authority is not an eval's and is judged elsewhere.
func evalAuthorityHolds(ctx context.Context, q database.Queryer, authority string, projectID ids.ID) (bool, error) {
	evalID, ok := evalOfAuthority(authority)
	if !ok {
		return false, nil
	}
	copyOf, err := evalCopyOfIn(ctx, q, projectID)
	return err == nil && copyOf == evalID, err
}

// rootAuthorityIn is the landing authority recorded on run or the nearest
// run that called it.
func rootAuthorityIn(ctx context.Context, q database.Queryer, runID ids.ID) (string, error) {
	queries := workflowsql.New(q.SQL())
	for id, depth := runID, 0; !id.IsZero() && depth <= maxCallDepth+1; depth++ {
		facts, err := queries.RunLandingAuthority(ctx, id)
		if err != nil {
			return "", database.Missing(err)
		}
		if facts.LandingAuthority != "" {
			return facts.LandingAuthority, nil
		}
		id = facts.ParentRunID
	}
	return "", nil
}

// evalGate is an eval's one admission rule for work in its copies: why a
// step of projectID may not start now ("" when it may, and always for a
// project that is no copy), or an error when that could not be read.
//
// It reads through q, the caller's transaction where one is open, so it
// never waits on another connection while that transaction holds one.
type evalGate interface {
	RefusalIn(ctx context.Context, q database.Queryer, projectID ids.ID) (string, error)
}

// EvalRun is a run the evals owner starts on a Change in one of its copies,
// with the eval's authority: its working lane, or "land".
type EvalRun struct {
	ChangeID  ids.ID
	Workflow  string
	EvalID    ids.ID
	StartedBy string
}

// StartEvalRun starts in on the copy's Change with the eval's authority
// recorded on the run. The caller needs run-start authority on the Change;
// the Change's project must be a copy of the eval, or nothing starts. The
// authority is judged again at every merge and deploy (assent.go).
func (s *Service) StartEvalRun(ctx context.Context, in EvalRun) (*Run, error) {
	if in.EvalID.IsZero() || strings.TrimSpace(in.StartedBy) == "" {
		return nil, fmt.Errorf("an eval's run names its eval and who started it")
	}
	action := actionStartRun
	if in.Workflow == "land" {
		action = actionRequestLanding
	}
	project, actor, err := s.changeScope(ctx, in.ChangeID, action)
	if err != nil {
		return nil, err
	}
	authority := evalAuthorityOf(in.EvalID, in.StartedBy)
	if holds, err := evalAuthorityHolds(ctx, s.db, authority, project); err != nil {
		return nil, err
	} else if !holds {
		return nil, fmt.Errorf("change #%d is not in a copy made for eval %d; its authority does not reach it", in.ChangeID, in.EvalID)
	}
	change, err := readChangeInProject(ctx, s.db, in.ChangeID, project)
	if err != nil {
		return nil, err
	}
	if change.State.Closed() || (in.Workflow == "land" && change.State != ChangeInProgress) {
		return nil, fmt.Errorf("change #%d is %s — it cannot run %s", change.ID, change.State, in.Workflow)
	}
	if s.starter == nil {
		return nil, fmt.Errorf("Run execution is unavailable")
	}
	return s.starter.start(ctx, StartRequest{ProjectID: project, ChangeID: change.ID, WorkflowName: in.Workflow, InputsJSON: "{}",
		requestedBy: actor.InferenceOwner, owner: actor.Owner, receiptActor: actor.Receipt, evalAuthority: authority})
}

// acceptedEvalAuthorityIn: a landing of accepted work in an eval's copy
// carries the eval's authority its work run was admitted with, so the
// landing proceeds as every run in the arm does. Any other accepted work
// carries none.
func acceptedEvalAuthorityIn(ctx context.Context, q database.Queryer, req StartRequest) (string, error) {
	if req.acceptedRunID.IsZero() {
		return "", nil
	}
	authority, err := rootAuthorityIn(ctx, q, req.acceptedRunID)
	if err != nil {
		return "", err
	}
	if holds, err := evalAuthorityHolds(ctx, q, authority, req.ProjectID); err != nil || !holds {
		return "", err
	}
	return authority, nil
}

// carryOrSettleForEval is the engine's hook for approvals another fact
// answers: an eval's authority in its copy, or else an accepted change's
// acceptance. An eval's run is settled by its eval, never by carrying.
func (s landings) carryOrSettleForEval(ctx context.Context, run *Run) (bool, error) {
	settled, err := s.settleForEval(ctx, run)
	if settled || err != nil {
		return settled, err
	}
	return s.carry(ctx, run)
}

// settleForEval passes every approval waiting in the run's current stage,
// once the stage's other steps have answered, when the run carries an eval's
// authority and its project is a copy of that eval. The evidence says
// eval-authorised and names the authority; it records no approver and no
// vote, so it is never a person's approval or acceptance. It reports whether
// any approval was settled, so the engine can advance.
func (s landings) settleForEval(ctx context.Context, run *Run) (bool, error) {
	if run == nil || run.Status != RunRunning {
		return false, nil
	}
	authority, err := rootAuthorityIn(ctx, s.db, run.ID)
	if err != nil {
		return false, err
	}
	if holds, err := evalAuthorityHolds(ctx, s.db, authority, run.ProjectID); err != nil || !holds {
		return false, err
	}
	steps, err := readSteps(ctx, s.db, run.ID)
	if err != nil {
		return false, err
	}
	if !settled(steps, run.CurrentStage) {
		return false, nil
	}
	return database.Transact(ctx, s.db, func(tx *database.Tx) (bool, error) {
		current, err := lockRunSnapshotIn(ctx, tx, run)
		if err != nil {
			return false, err
		}
		locked, err := stepRecords(workflowsql.New(tx.SQL()).StageStepsForUpdate(ctx, workflowsql.StageStepsForUpdateParams{RunID: current.ID, Stage: current.CurrentStage}))
		if err != nil {
			return false, err
		}
		// In a checked landing an approval is assent to the pinned
		// candidate, as a person's vote is (approveIn): the merge asks that
		// every approval names the commit it lands.
		candidate := ""
		if c, pinned, err := LandingCandidateIn(ctx, tx, current.ID); err != nil {
			return false, err
		} else if pinned {
			candidate = c.Candidate
		}
		settledAny := false
		for _, step := range locked {
			if step.Type != "approval" || step.Status != StatusPending || step.OverrideBy != "" {
				continue
			}
			at := s.now()
			outcome, err := step.settleForEval(authority, candidate, at)
			if err != nil {
				return false, err
			}
			n, err := workflowsql.New(tx.SQL()).RecordApproval(ctx, workflowsql.RecordApprovalParams{At: at,
				ID: step.ID, Attempt: step.Attempt, ExpectedStatus: step.Status.String(), Status: outcome.Status.String(), EvidenceJson: outcome.Evidence,
			})
			if err != nil {
				return false, err
			}
			if n != 1 {
				return false, ErrRunMoved
			}
			if err := recordReceiptIn(ctx, tx, current.ID, "approval.eval-authorised", evalAuthorisedReceipt{Step: step.Name, Authority: authority}, s.now()); err != nil {
				return false, err
			}
			settledAny = true
		}
		return settledAny, nil
	})
}

type evalAuthorisedReceipt struct {
	Step      string `json:"step"`
	Authority string `json:"authority"`
}

// settleForEval passes an undecided approval on an eval's authority: the
// round closes approved with the step's stated meaning, the authority named,
// the landing's candidate when it has one, and no approver or vote.
func (s *Step) settleForEval(authority, candidate string, at time.Time) (approvalOutcome, error) {
	if s.Type != "approval" || s.OverrideBy != "" || s.Status == StatusPass || s.Status == StatusFail {
		return approvalOutcome{}, fmt.Errorf("step %q is not an undecided approval: %w", s.Name, errStepDecisionConflict)
	}
	cfg, err := ReadApprovalConfig(s.Config)
	if err != nil {
		return approvalOutcome{}, err
	}
	document, err := readApprovalDocument(s.Evidence)
	if err != nil {
		return approvalOutcome{}, err
	}
	if _, err := s.Status.transition(moveStepVote, StatusPass); err != nil {
		return approvalOutcome{}, err
	}
	next := document.evidence.clone()
	next.Quorum, next.Means = cfg.Quorum, cfg.Means
	next.Approver, next.Comment, next.Candidate = "", "", candidate
	next.Decision, next.At, next.EvalAuthorised = VoteApprove, at.UTC().Format(time.RFC3339Nano), authority
	body, err := document.encode(next)
	if err != nil {
		return approvalOutcome{}, err
	}
	return approvalOutcome{Round: next, Evidence: body, Status: StatusPass}, nil
}
