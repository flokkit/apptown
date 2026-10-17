package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
)

var ErrStepAttemptLost = errors.New("step attempt is no longer current")

// stepAttempts owns execution attempts inside an already-admitted active Run.
// The execution registry admits runner kinds; a committed claim is required to
// finish. This owner does not authorize creating or changing a Run.
type stepAttempts struct {
	db        *database.DB
	automatic func(string) bool
	clocked
}

func newStepAttempts(db *database.DB, automatic func(string) bool) stepAttempts {
	return stepAttempts{db: db, automatic: automatic}
}
func (s stepAttempts) Start(ctx context.Context, snapshot *Step, kind string) (*Step, error) {
	return s.start(ctx, snapshot, kind, ids.Nil)
}
func (s stepAttempts) start(ctx context.Context, snapshot *Step, kind string, projectID ids.ID) (*Step, error) {
	if snapshot == nil || s.automatic == nil {
		return nil, ErrStepAttemptLost
	}
	claim, err := database.Transact(ctx, s.db, func(tx *database.Tx) (*Step, error) {
		run, current, err := lockStep(ctx, tx, snapshot.ID)
		if err != nil {
			return nil, err
		}
		if err := refuseUnlessActive(ctx, tx, run.ProjectID); err != nil {
			return nil, err
		}
		if (!projectID.IsZero() && run.ProjectID != projectID) || !s.automatic(current.Type) || run.Status != RunRunning || run.CurrentStage != current.Stage || current.Status != StatusPending || current.OverrideBy != "" || current.RunID != snapshot.RunID || current.Type != snapshot.Type || current.Attempt != snapshot.Attempt || !bytes.Equal(current.Config, snapshot.Config) || (kind != "" && kind != current.Type) {
			return nil, fmt.Errorf("step %d: %w", snapshot.ID, ErrStepAttemptLost)
		}
		if _, err := current.Status.Transition(MoveStepStart, StatusRunning); err != nil {
			return nil, err
		}
		payload := stepStartedReceipt{Step: current.Name, Stage: current.Stage, Type: current.Type, Attempt: current.Attempt + 1}
		// An agent's or a reviewer's session is named by its start receipt;
		// its kept transcript is recorded against that name.
		if current.Type == "agent/pi" || current.Type == "agent/review" {
			payload.SessionID, err = nextAgentSession(ctx, tx, current)
			if err != nil {
				return nil, err
			}
		}
		n, err := workflowsql.New(tx.SQL()).StartAttempt(ctx, workflowsql.StartAttemptParams{At: s.now(), ID: current.ID, Attempt: current.Attempt})
		if err != nil {
			return nil, err
		}
		if n != 1 {
			return nil, ErrStepAttemptLost
		}
		generation, err := claimSourceAuthorityIn(ctx, tx, run, current.Attempt > 0)
		if err != nil {
			return nil, err
		}
		// The receipt keeps the generation, so a restarted forge collecting
		// this attempt's task publishes under the same authority.
		payload.SourceGeneration = generation
		if err := recordStartReceiptIn(ctx, tx, run.ID, payload, s.now()); err != nil {
			return nil, err
		}
		claimed, err := stepResult(workflowsql.New(tx.SQL()).StepByID(ctx, current.ID))
		if err == nil {
			claimed.ownedProjectID, claimed.ownedSourceGeneration = run.ProjectID, generation
		}
		return claimed, err
	})
	if err != nil {
		return nil, err
	}
	// Issue ownership only after the complete admission transaction commits.
	claim.ownedAttempt = claim.Attempt
	claim.ownedStepID, claim.ownedRunID = claim.ID, claim.RunID
	return claim, nil
}

type stepStartedReceipt struct {
	Step             string `json:"step"`
	Stage            string `json:"stage"`
	Type             string `json:"type"`
	Attempt          int64  `json:"attempt"`
	SessionID        string `json:"sessionId,omitempty"`
	SourceGeneration int64  `json:"sourceGeneration,omitempty"`
}

func recordStartReceiptIn(ctx context.Context, tx *database.Tx, runID ids.ID, payload stepStartedReceipt, at time.Time) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return recordEventIn(ctx, tx, runID, "step.started", encoded, at)
}
