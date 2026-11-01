package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
)

// LandingRecheck is an already-admitted retry, optionally bound to a selected Run.
type LandingRecheck struct {
	ChangeID    ids.ID
	who         string
	RunID       ids.ID
	Remediation *LandingRetry
}

type LandingRetry struct {
	ItemID           ids.ID
	Attempt, Retries int
	Reason           string
}

// LandingRechecked names every step a recheck runs again (Names) and,
// of those, the gates that had failed (Failed). A recheck also runs the
// passed catch-up step again, so it checks the branch as it is now; that
// step is in Names, not in Failed.
type LandingRechecked struct {
	RunID  ids.ID
	Names  []string
	Failed []string
}

// LandingWork publishes a change folder's committed work under its
// holder's grant before a landing picks its candidate (the chat's, or a
// momentary request's when nobody holds the change).
type LandingWork interface {
	PublishForLanding(ctx context.Context, change *Change) error
}

// RecheckLanding re-runs a landing's gates for the change; the actor is
// whoever the context names, never the request.
func (s *Service) RecheckLanding(ctx context.Context, in LandingRecheck) (LandingRechecked, error) {
	projectID, actor, err := s.changeScope(ctx, in.ChangeID, actionRequestLanding)
	if err != nil {
		return LandingRechecked{}, err
	}
	in.who = actor.Receipt
	// Publication is an effect too: refuse an unprepared landing before Git,
	// then recheck under the reset transaction's locks below.
	if err := s.db.InTx(ctx, func(tx *database.Tx) error {
		run, err := runRecord(workflowsql.New(tx.SQL()).ActiveLandingForUpdate(ctx, workflowsql.ActiveLandingForUpdateParams{ChangeID: in.ChangeID, ProjectID: projectID, Status: RunRunning.String()}))
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("change %d has no landing in progress — start one with apptown land", in.ChangeID)
		}
		if err != nil {
			return err
		}
		if !in.RunID.IsZero() && run.ID != in.RunID {
			return ErrRunMoved
		}
		return requireRunReady(ctx, tx, run.ID)
	}); err != nil {
		return LandingRechecked{}, err
	}
	// The fix the chat committed reaches the branch before the recheck picks
	// its candidate; the landing itself only reads the branch.
	if s.landingWork != nil {
		change, err := ReadChangeIn(ctx, s.db, in.ChangeID)
		if err != nil {
			return LandingRechecked{}, err
		}
		if err := s.landingWork.PublishForLanding(ctx, change); err != nil {
			return LandingRechecked{}, err
		}
	}
	out, err := s.landingRechecks.Recheck(ctx, in)
	if err == nil && len(out.Names) > 0 {
		s.DispatchCommittedRetry(out.RunID)
	}
	return out, err
}

// RecheckLandingIn joins an already-authorized operation in the caller's transaction.
// It verifies attribution; the caller owns authorization and commit, then calls
// DispatchCommittedRetry if the recheck reset any steps.
func (s *Service) RecheckLandingIn(ctx context.Context, tx *database.Tx, in LandingRecheck) (LandingRechecked, error) {
	actor, err := s.requireActor(ctx, operationPermission{Action: actionIdentify})
	if err != nil {
		return LandingRechecked{}, err
	}
	in.who = actor.Receipt
	return s.landingRechecks.RecheckIn(ctx, tx, in)
}

// LandingRetryBudget owns external retry accounting; Work owns check selection.
type LandingRetryBudget interface {
	ChargeIn(context.Context, *database.Tx, ids.ID, ids.ID, LandingRetry) error
}

type landingRechecks struct {
	db        *database.DB
	automatic []string
	budget    LandingRetryBudget
	clocked
}

func newLandingRechecks(db *database.DB, automatic []string, budget LandingRetryBudget) landingRechecks {
	return landingRechecks{db: db, automatic: slices.Clone(automatic), budget: budget}
}

// Recheck intent, old evidence, receipts and the advancement fence commit
// together. No runner starts until this transaction has committed.
func (s landingRechecks) Recheck(ctx context.Context, req LandingRecheck) (LandingRechecked, error) {
	req = req.frozen()
	return database.Transact(ctx, s.db, func(tx *database.Tx) (LandingRechecked, error) {
		return s.recheckIn(ctx, tx, req)
	})
}

// Reconciliation can include the recheck in its guarded action transaction.
func (s landingRechecks) RecheckIn(ctx context.Context, tx *database.Tx, req LandingRecheck) (LandingRechecked, error) {
	return s.recheckIn(ctx, tx, req.frozen())
}

func (s landingRechecks) recheckIn(ctx context.Context, tx *database.Tx, req LandingRecheck) (LandingRechecked, error) {
	var result LandingRechecked
	q := workflowsql.New(tx.SQL())
	projectID, err := q.ChangeProjectMeta(ctx, req.ChangeID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return result, fmt.Errorf("no such change: %d", req.ChangeID)
		}
		return result, err
	}
	if _, err := q.ProjectForUpdate(ctx, projectID); err != nil {
		return result, err
	}
	change, err := changeRecord(q.ChangeForUpdate(ctx, workflowsql.ChangeForUpdateParams{ID: req.ChangeID}))
	if err != nil {
		return result, err
	}
	if change.ProjectID != projectID {
		return result, ErrRunMoved
	}
	if !change.State.CanRun() {
		return result, fmt.Errorf("change %d is %s; it cannot be rechecked", req.ChangeID, change.State)
	}
	run, err := runRecord(workflowsql.New(tx.SQL()).ActiveLandingForUpdate(ctx, workflowsql.ActiveLandingForUpdateParams{ChangeID: req.ChangeID, ProjectID: projectID, Status: RunRunning.String()}))
	if errors.Is(err, sql.ErrNoRows) {
		return result, fmt.Errorf("change %d has no landing in progress — start one with apptown land", req.ChangeID)
	}
	if err != nil {
		return result, err
	}
	if err := requireRunReady(ctx, tx, run.ID); err != nil {
		return result, err
	}
	result.RunID = run.ID
	if !req.RunID.IsZero() && req.RunID != result.RunID {
		return result, fmt.Errorf("the landing changed before it could be rechecked")
	}
	spec, err := run.Definition()
	if err != nil {
		return result, err
	}
	stage, _ := spec.Stage(run.CurrentStage)
	if stage == nil {
		return result, fmt.Errorf("stage %q is missing from the pinned definition", run.CurrentStage)
	}
	steps, err := stepRecords(workflowsql.New(tx.SQL()).StageStepsForUpdate(ctx, workflowsql.StageStepsForUpdateParams{RunID: run.ID, Stage: run.CurrentStage}))
	if err != nil {
		return result, err
	}
	var selected []*Step
	blockedReasons := map[string]bool{}
	for _, step := range steps {
		if step.Status == StatusRunning {
			return result, fmt.Errorf("wait for the running steps to finish before rechecking")
		}
		if !slices.Contains(s.automatic, step.Type) || step.OverrideBy != "" {
			continue
		}
		entry := entryStep(stage, step)
		var evidence struct {
			Reason string `json:"reason"`
		}
		if step.Status == StatusSkipped {
			if err := json.Unmarshal(step.Evidence, &evidence); err != nil {
				return result, err
			}
		}
		if step.Status == StatusFail || step.Status == StatusError || (entry && step.Status == StatusSkipped && blockedReasons[evidence.Reason]) {
			selected = append(selected, step)
			if entry {
				blockedReasons[skipReason(step.Name)] = true
			}
		}
	}
	if len(selected) == 0 {
		return result, nil
	}
	for _, step := range selected {
		result.Failed = append(result.Failed, step.Name)
	}
	// A recheck always catches up again, so it checks the change as it is
	// now: catch-up pins the candidate, and a new candidate reruns the gates
	// that passed on the old one. The same candidate reruns only what failed.
	for _, step := range steps {
		if step.Type == "reconcile" && step.OverrideBy == "" && !slices.Contains(selected, step) && (step.Status == StatusPass || step.Status == StatusSkipped) {
			selected = append([]*Step{step}, selected...)
		}
	}
	if retry := req.Remediation; retry != nil {
		if s.budget == nil {
			return result, fmt.Errorf("landing retry accounting is unavailable")
		}
		if err := s.budget.ChargeIn(ctx, tx, projectID, req.ChangeID, *retry); err != nil {
			return result, err
		}
	}
	for _, step := range selected {
		if err := recheckStepIn(ctx, tx, step); err != nil {
			return LandingRechecked{}, err
		}
		payload := landingRecheckReceipt{Step: step.Name, StepID: step.ID, Stage: step.Stage, By: req.who, Attempt: step.Attempt, PreviousStatus: step.Status, PreviousEvidence: step.Evidence}
		if req.Remediation != nil {
			payload.RemediationItemID = req.Remediation.ItemID
		}
		if err := recordReceiptIn(ctx, tx, run.ID, "step.recheck", payload, s.now()); err != nil {
			return LandingRechecked{}, err
		}
		result.Names = append(result.Names, step.Name)
	}
	if err := invalidateRunDecisionIn(ctx, tx, run, s.now()); err != nil {
		return LandingRechecked{}, err
	}
	return result, nil
}

type landingRecheckReceipt struct {
	Step              string          `json:"step"`
	StepID            ids.ID          `json:"stepId"`
	Stage             string          `json:"stage"`
	By                string          `json:"by"`
	Attempt           int64           `json:"attempt"`
	PreviousStatus    StepState       `json:"previousStatus"`
	PreviousEvidence  json.RawMessage `json:"previousEvidence"`
	RemediationItemID ids.ID          `json:"remediationItemId,omitzero"`
}

func (in LandingRecheck) frozen() LandingRecheck {
	if in.Remediation != nil {
		retry := *in.Remediation
		in.Remediation = &retry
	}
	return in
}
