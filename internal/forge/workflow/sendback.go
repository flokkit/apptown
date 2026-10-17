package workflow

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
	"flokkit.io/forge/internal/strs"
)

type SendBack struct {
	ChangeID ids.ID
	Note     string
}
type sendBackStore struct {
	db           *database.DB
	automatic    []string
	abandonments Abandonments
	chats        Presence
	clocked
}

func (s *Service) SendBack(ctx context.Context, in SendBack) (Retried, error) {
	if _, err := s.require(ctx, operationPermission{Action: actionIdentify}); err != nil {
		return Retried{}, err
	}
	project, err := workflowsql.New(s.db.SQL()).ChangeProjectMeta(ctx, in.ChangeID)
	if err != nil {
		return Retried{}, database.Missing(err)
	}
	actor, err := s.requireActor(ctx, operationPermission{Action: actionSendBackChange, ProjectID: project, ChangeID: in.ChangeID})
	if err != nil {
		return Retried{}, err
	}
	landingBy := s.landingBy(ctx, StartRequest{ProjectID: project, ChangeID: in.ChangeID})
	result, err := s.sendbacks.sendBack(ctx, in.ChangeID, in.Note, actor.Receipt, landingBy, project)
	if err == nil {
		s.DispatchCommittedRetry(result.RunID)
	}
	return result, err
}

type sendBackReceipt struct {
	Note        string `json:"note"`
	SourceRunID ids.ID `json:"sourceRunId"`
	StepID      ids.ID `json:"stepId"`
}

func sendBackSummary(note string) string {
	return strs.Truncate(note, 120)
}

// Selection, landing abandonment, continuation creation and feedback share the
// project's completion lock. Neither stale HTTP objects nor old landing jobs
// can make the change point back at superseded work.
func (s sendBackStore) sendBack(ctx context.Context, changeID ids.ID, note, who, landingBy string, expected ids.ID) (Retried, error) {
	now := s.now()
	var result Retried
	if strings.TrimSpace(note) == "" {
		return result, &RetryError{RetryInvalid, "say what to change — the note is what the agent works from"}
	}
	projectID, err := workflowsql.New(s.db.SQL()).ChangeProjectMeta(ctx, changeID)
	if err != nil {
		return result, database.Missing(err)
	}
	closure, err := scm.PrepareClosure(ctx, s.abandonments.locks, projectID)
	if err != nil {
		return result, err
	}
	defer closure.Close()
	return database.Transact(ctx, s.db, func(tx *database.Tx) (Retried, error) {
		q := workflowsql.New(tx.SQL())
		if _, err := q.ProjectForUpdate(ctx, projectID); err != nil {
			return result, err
		}
		if err := refuseUnlessActive(ctx, tx, projectID); err != nil {
			return result, err
		}
		change, err := q.ChangeForUpdate(ctx, workflowsql.ChangeForUpdateParams{ID: changeID})
		if err != nil {
			return result, err
		}
		state, err := ParseChangeState(change.State)
		if err != nil {
			return result, err
		}
		lockedProject := change.ProjectID
		if lockedProject != projectID || (!expected.IsZero() && expected != projectID) {
			return result, &RetryError{RetryConflict, "Change scope changed after authorization"}
		}
		if !state.CanRun() {
			return result, &RetryError{RetryConflict, fmt.Sprintf("change #%d is %s; only open work can be sent back", changeID, state)}
		}
		if err := checkClosureIn(ctx, tx, closure, projectID, changeID, ids.Nil); err != nil {
			return result, &RetryError{RetryConflict, err.Error()}
		}
		// A child or abandoned run is never a replacement for the working root.
		run, err := runRecord(workflowsql.New(tx.SQL()).WorkingRunForUpdate(ctx, changeID))
		if errors.Is(err, sql.ErrNoRows) {
			return result, &RetryError{RetryUnavailable, "this change has no working run to send back to — open a chat about it instead"}
		}
		if err != nil {
			return result, err
		}
		if run.ProjectID != projectID || run.ChangeID != changeID {
			return result, &RetryError{RetryConflict, "working run scope changed"}
		}
		agent, err := stepResult(workflowsql.New(tx.SQL()).RunAgentForUpdate(ctx, run.ID))
		if errors.Is(err, sql.ErrNoRows) {
			return result, &RetryError{RetryUnavailable, "this working run has no agent step to send back to"}
		}
		if err != nil {
			return result, err
		}
		if agent.Status == StatusPending || agent.Status == StatusRunning {
			return result, &RetryError{RetryConflict, "the agent is already queued or working on it"}
		}
		busy, err := q.SendBackBusy(ctx, changeID)
		if err != nil {
			return result, err
		}
		if busy {
			return result, &RetryError{RetryConflict, "wait for the running steps to finish before sending the work back"}
		}
		if run.Status == RunRunning && run.CurrentStage == agent.Stage {
			if _, err := newStepRetries(s.db, s.automatic).RetryIn(ctx, tx, retryIntent{RetryCommand: RetryCommand{StepID: agent.ID, Mode: RetryContinue, Feedback: note}, RunID: run.ID, Actor: commandActor{Receipt: who}}, now); err != nil {
				return result, err
			}
			result = Retried{run.ID, agent.ID}
		} else {
			// Stop any root being replaced, including all its active descendants.
			roots, err := q.SendBackActiveRoots(ctx, changeID)
			if err != nil {
				return result, err
			}
			for _, id := range roots {
				if _, err := s.abandonments.abandonIn(ctx, tx, Abandonment{RunID: id, Who: who, Reason: "sent back to the agent with a note: " + sendBackSummary(note)}, projectID, closure); err != nil {
					return result, err
				}
			}
			result, err = continueWorkIn(ctx, tx, run, agent, note, who, landingBy, s.chats, now)
			if err != nil {
				return result, err
			}
		}
		if _, err := TransitionChangeIn(ctx, tx, projectID, changeID, ChangeMove{Action: ResumeChange, RunID: result.RunID}, now); err != nil {
			return result, err
		}
		if err := audit.At(now).RecordIn(ctx, tx, who, projectID, "change.sendback", fmt.Sprintf("change %d run %d", changeID, result.RunID), sendBackReceipt{note, run.ID, result.StepID}); err != nil {
			return result, err
		}
		return result, nil
	})
}
