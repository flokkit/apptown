package workflow

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
)

type reworkStore struct {
	db *database.DB
	clocked
}

type reworkedResult struct {
	RunID   ids.ID
	AgentID ids.ID
}

// requestIn records a single continuation. Pending agent state is the durable
// dispatch intent, understood by normal stage recovery without another worker.
func (s reworkStore) requestIn(ctx context.Context, tx *database.Tx, run *Run, source *Step, human bool) (reworkedResult, error) {
	result := reworkedResult{RunID: run.ID}
	if run.Status != RunRunning || run.CurrentStage != source.Stage || source.Status != StatusFail || source.OverrideBy != "" {
		return result, nil
	}
	agent, err := stepResult(workflowsql.New(tx.SQL()).StageAgentForUpdate(ctx, workflowsql.StageAgentForUpdateParams{RunID: run.ID, Stage: source.Stage}))
	if errors.Is(err, sql.ErrNoRows) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	if agent.Status != StatusPass && agent.Status != StatusFail {
		return result, nil
	}
	kind := "step.finished"
	if human {
		kind = "approval"
	}
	history, err := workflowsql.New(tx.SQL()).ReworkHistory(ctx, workflowsql.ReworkHistoryParams{
		RunID: run.ID, SourceID: source.ID, TriggerKind: kind, SourceName: source.Name, AgentName: agent.Name,
	})
	if err != nil {
		return result, err
	}
	if history.Seen {
		return result, nil
	}
	if !human {
		if history.Rounds >= 3 {
			return result, nil
		}
		// A late verdict cannot start another revision of work it never examined.
		if !history.CheckStart.IsZero() && history.CheckStart.Compare(history.AgentStart) < 0 {
			return result, nil
		}
		if source.FinishedAt.Before(agent.FinishedAt) {
			return result, nil
		}
	}
	if human {
		if err := resetApprovalIn(ctx, tx, source); err != nil {
			return result, err
		}
	}
	if _, err := agent.Status.Transition(MoveStepReset, StatusPending); err != nil {
		return result, err
	}
	if err := requireAttemptWrite(workflowsql.New(tx.SQL()).ReworkAgent(ctx, workflowsql.ReworkAgentParams{ID: agent.ID, Attempt: agent.Attempt})); err != nil {
		return result, err
	}
	reason := source.Type + " failed — evidence fed back to the agent"
	if human {
		reason = "rejected by a person — their words fed back to the agent"
	}
	if err := recordReceiptIn(ctx, tx, run.ID, "step.rework", reworkReceipt{source.Name, agent.ID, reason, source.ID, history.TriggerEvent, agent.OverrideBy, agent.OverrideReason}, s.now()); err != nil {
		return result, err
	}
	if err := invalidateRunDecisionIn(ctx, tx, run, s.now()); err != nil {
		return result, err
	}
	result.AgentID = agent.ID
	return result, nil
}

// Machine runners select which failures warrant rework. The verdict and the
// continuation commit together, so a restart cannot lose that decision.
func (s reworkStore) fail(ctx context.Context, claim *Step, outcome StepOutcome) (reworkedResult, error) {
	return database.Transact(ctx, s.db, func(tx *database.Tx) (reworkedResult, error) {
		finished, err := newStepAttempts(s.db, nil).FinishIn(ctx, tx, claim, outcome)
		if err != nil {
			return reworkedResult{}, err
		}
		return s.requestIn(ctx, tx, finished.Run, finished.Step, false)
	})
}

// Preserve all rounds of votes, including the rejection used as feedback.
func resetApprovalIn(ctx context.Context, tx *database.Tx, step *Step) error {
	document, err := readApprovalDocument(step.Evidence)
	if err != nil {
		return err
	}
	evidence, err := document.evidence.reopen(step.Status)
	if err != nil {
		return err
	}
	body, err := document.encode(evidence)
	if err != nil {
		return err
	}
	return requireAttemptWrite(workflowsql.New(tx.SQL()).ReopenApproval(ctx, workflowsql.ReopenApprovalParams{EvidenceJson: body, ID: step.ID, Attempt: step.Attempt}))
}

// The Work command owns admission; this participant owns the canonical vote,
// receipt and any rejection/rework in the caller's transaction.
func (s reworkStore) approveIn(ctx context.Context, tx *database.Tx, run *Run, step *Step, vote ApprovalVote) (reworkedResult, error) {
	user, decision := vote.Approver, vote.Decision
	result := reworkedResult{RunID: run.ID}
	if err := run.Decides(step); err != nil {
		return result, fmt.Errorf("approval is not in an active workflow stage: %w", err)
	}
	if step.Type == "approval" && step.Status != StatusPass && step.Status != StatusFail {
		// A rejected round reopened with an agent still revising the work is
		// not ready for a decision: that is a fact about the stage's other
		// steps, which the step itself cannot see.
		document, err := readApprovalDocument(step.Evidence)
		if err != nil {
			return result, err
		}
		if document.evidence.LastRejection != nil {
			revising, err := workflowsql.New(tx.SQL()).RevisingAgentExists(ctx, workflowsql.RevisingAgentExistsParams{RunID: run.ID, Stage: step.Stage})
			if err != nil {
				return result, err
			}
			if revising {
				return result, fmt.Errorf("the agent is still revising this work; decide after it finishes: %w", errStepDecisionConflict)
			}
		}
	}
	// A vote in a checked landing is assent to its pinned candidate, whether
	// cast by a person or carried from an acceptance.
	if c, pinned, err := LandingCandidateIn(ctx, tx, run.ID); err != nil {
		return result, err
	} else if pinned {
		if vote.Adds != nil && vote.Adds.Head != c.Candidate {
			// The list was read for another candidate than the one this
			// vote would assent to.
			return result, fmt.Errorf("the landing's candidate moved since what it adds was read: %w", errAddsChanged)
		}
		vote.Candidate = c.Candidate
	}
	// One AppTown time for the vote and the step's decision.
	at := s.now()
	outcome, err := step.Approve(vote, at)
	if err != nil {
		return result, err
	}
	evidence, status, pending := outcome.Round, outcome.Status, outcome.Pending
	n, err := workflowsql.New(tx.SQL()).RecordApproval(ctx, workflowsql.RecordApprovalParams{At: at,
		ID: step.ID, Attempt: step.Attempt, ExpectedStatus: step.Status.String(), Status: status.String(), EvidenceJson: outcome.Evidence,
	})
	if err != nil {
		return result, err
	}
	if n != 1 {
		return result, fmt.Errorf("this approval changed before the vote was recorded: %w", errStepDecisionConflict)
	}
	payload := approvalReceipt{Step: step.Name, By: user, Via: vote.Via, Decision: decision}
	if pending {
		payload.Progress = fmt.Sprintf("%d of %d", evidence.Approvals, evidence.Quorum)
	}
	if err := recordReceiptIn(ctx, tx, step.RunID, "approval", payload, s.now()); err != nil {
		return result, err
	}
	if decision == VoteReject {
		step, err = stepResult(workflowsql.New(tx.SQL()).StepForUpdate(ctx, workflowsql.StepForUpdateParams{ID: step.ID}))
		if err != nil {
			return result, err
		}
		result, err = s.requestIn(ctx, tx, run, step, true)
		if err != nil {
			return result, err
		}
	}
	return result, nil
}

type approvalReceipt struct {
	Step string `json:"step"`
	By   string `json:"by"`
	// Via is how the vote was cast (access.Actor.Via): a CLI token's name
	// says which credential pressed it, which "by" alone does not.
	Via      string           `json:"via,omitempty"`
	Decision ApprovalDecision `json:"decision"`
	Progress string           `json:"progress,omitempty"`
}

type reworkReceipt struct {
	Step                        string `json:"step"`
	AgentStep                   ids.ID `json:"agentStep"`
	Reason                      string `json:"reason"`
	TriggerStep                 ids.ID `json:"triggerStep"`
	TriggerEvent                ids.ID `json:"triggerEvent"`
	PreviousAgentOverrideBy     string `json:"previousAgentOverrideBy"`
	PreviousAgentOverrideReason string `json:"previousAgentOverrideReason"`
}

// Only committed commands schedule rework. DispatchAgain coalesces a request
// made by a still-running stage, and never extends the shutdown wait bound.
func (s *Service) dispatchRework(result reworkedResult) {
	if !result.AgentID.IsZero() {
		s.dispatcher.DispatchAgain(DispatchPlan{RunID: result.RunID, Steps: []ReadyStep{{ID: result.AgentID, Type: "agent/pi"}}})
	}
}
