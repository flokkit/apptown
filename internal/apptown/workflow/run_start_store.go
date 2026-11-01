package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
)

// landingOccupied refuses a landing while another change of the project is
// landing: one landing at a time. A client that waits its turn reads the code.
type landingOccupied struct{}

func (landingOccupied) Error() string         { return "one landing at a time: another change is landing" }
func (landingOccupied) FaultKind() fault.Kind { return fault.Conflict }
func (landingOccupied) FaultCode() string     { return "landing-occupied" }

type runStartStore struct {
	db     *database.DB
	stages stages
	chats  Presence // whether a holder of a change has ended
	clocked
}

// Replays with a durable identity reuse the original run. An ordinary start
// has no replay identity: a second concurrent request is refused, not guessed.
func (s runStartStore) existing(ctx context.Context, req StartRequest) (*Run, error) {
	return existingStartedRun(ctx, s.db, req)
}
func existingStartedRun(ctx context.Context, db database.Queryer, req StartRequest) (*Run, error) {
	queries := workflowsql.New(db.SQL())
	var row workflowsql.Run
	var err error
	switch {
	case !req.acceptedRunID.IsZero():
		row, err = queries.StartedLanding(ctx, workflowsql.StartedLandingParams{WorkRunID: req.acceptedRunID, ProjectID: req.ProjectID, ChangeID: req.ChangeID})
	case !req.parentStepID.IsZero():
		row, err = queries.StartedChild(ctx, workflowsql.StartedChildParams{ParentRunID: req.parentRunID, ParentStepID: req.parentStepID, ProjectID: req.ProjectID, ChangeID: req.ChangeID, ParentStepAttempt: req.parentStepAttempt + 1})
	default:
		return nil, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return runRecord(row, err)
}

func (s runStartStore) create(ctx context.Context, req StartRequest, def PreparedRun) (*Run, error) {
	return database.Transact(ctx, s.db, func(tx *database.Tx) (*Run, error) {
		return s.createIn(ctx, tx, req, def)
	})
}

// createIn lets a caller commit its ownership records with the run and stage.
// Preparation and dispatch remain outside the caller's transaction.
func (s runStartStore) createIn(ctx context.Context, tx *database.Tx, req StartRequest, def PreparedRun) (*Run, error) {
	if !req.parentRunID.IsZero() && def.Spec.Kind != "pipeline" {
		return nil, fmt.Errorf("callee %q is a %s; only pipeline workflows are callable", req.WorkflowName, def.Spec.Kind)
	}
	if !req.acceptedRunID.IsZero() && (!req.parentRunID.IsZero() || def.Spec.Role != "landing") {
		return nil, fmt.Errorf("accepted work must start a root landing")
	}
	def, definition, err := freezeRunDefinition(def)
	if err != nil {
		return nil, err
	}
	if err := validateStart(req); err != nil {
		return nil, err
	}

	// All creation contenders take the project lock before change/run/step
	// locks, matching completion and adoption. External work is outside it.
	queries := workflowsql.New(tx.SQL())
	if _, err := queries.ProjectForUpdate(ctx, req.ProjectID); err != nil {
		return nil, err
	}
	if err := refuseUnlessActive(ctx, tx, req.ProjectID); err != nil {
		return nil, err
	}
	// Read the revision in a fresh statement after acquiring the lock: an
	// adoption that committed while we waited must be visible to this check.
	currentAdoption, err := adoption.CurrentReceiptIDIn(ctx, tx, req.ProjectID)
	if err != nil {
		return nil, err
	}
	if existing, err := existingStartedRun(ctx, tx, req); err != nil {
		return nil, err
	} else if existing != nil {
		return existing, nil
	}
	expectedAdoption := currentAdoption
	var parentStepName, parentStage string
	if !req.parentRunID.IsZero() {
		parent, err := runRecord(queries.RunForUpdate(ctx, req.parentRunID))
		if err != nil {
			return nil, err
		}
		if parent.ProjectID != req.ProjectID || parent.ChangeID != req.ChangeID || parent.Status != RunRunning {
			return nil, fmt.Errorf("parent run %d is not active in this change", req.parentRunID)
		}
		source, err := ReadSourceIn(ctx, tx, RunRef{ID: req.parentRunID, ProjectID: req.ProjectID})
		if err != nil {
			return nil, err
		}
		if source.Depth >= MaxCallDepth {
			return nil, fmt.Errorf("call depth cap (%d) reached — check for workflow cycles", MaxCallDepth)
		}
		if !parent.AdoptionID.IsZero() {
			expectedAdoption = parent.AdoptionID
		}
		if err := requireRunReady(ctx, tx, req.parentRunID); err != nil {
			return nil, err
		}
		if !req.parentStepID.IsZero() {
			step, err := stepResult(queries.StepForUpdate(ctx, workflowsql.StepForUpdateParams{ID: req.parentStepID, RunID: req.parentRunID}))
			if err != nil {
				return nil, err
			}
			// JSONB storage spacing is not part of configuration identity. Compact
			// without decoding numbers through float64, just as the snapshot does.
			if step.Type != "workflow/call" || step.Stage != parent.CurrentStage || step.Status != StatusPending || step.Attempt != req.parentStepAttempt || step.OverrideBy != "" || (req.parentStepConfig != "" && req.parentStepConfig != string(step.Config)) {
				return nil, fmt.Errorf("parent call step %d is no longer runnable", req.parentStepID)
			}
			if _, err := step.Status.transition(moveStepStart, StatusRunning); err != nil {
				return nil, err
			}
			parentStage, parentStepName = step.Stage, step.Name
		}
	} else if !req.ChangeID.IsZero() {
		change, err := changeRecord(queries.ChangeForUpdate(ctx, workflowsql.ChangeForUpdateParams{ID: req.ChangeID, ProjectID: req.ProjectID}))
		if err != nil {
			return nil, err
		}
		if !change.State.canRun() {
			return nil, fmt.Errorf("change #%d is %s — only in-progress changes take runs", req.ChangeID, change.State)
		}
		if !change.TargetAdoptionID.IsZero() {
			expectedAdoption = change.TargetAdoptionID
		}
		active, err := queries.ActiveChangeRunMeta(ctx, workflowsql.ActiveChangeRunMetaParams{ChangeID: req.ChangeID, Status: RunRunning.String()})
		if err != nil {
			return nil, database.Missing(err)
		}
		if !active.IsZero() {
			return nil, fmt.Errorf("change #%d already has a run in flight (run %d)", req.ChangeID, active)
		}
	}
	if expectedAdoption != def.AdoptionID {
		return nil, fmt.Errorf("the project's way of working changed while starting the run; start again")
	}
	if !req.acceptedRunID.IsZero() {
		superseded, err := queries.WasRunSentBack(ctx, req.acceptedRunID)
		if err != nil {
			return nil, err
		}
		if superseded {
			return nil, fmt.Errorf("run %d was sent back; its acceptance no longer starts landing", req.acceptedRunID)
		}
		valid, err := queries.IsAcceptedWorkingRun(ctx, workflowsql.IsAcceptedWorkingRunParams{ID: req.acceptedRunID, ProjectID: req.ProjectID, ChangeID: req.ChangeID, Status: RunDone.String()})
		if err != nil {
			return nil, err
		}
		if !valid {
			return nil, fmt.Errorf("run %d is not completed work for this change", req.acceptedRunID)
		}
		if err := queries.ReserveLanding(ctx, req.acceptedRunID); err != nil {
			return nil, err
		}
	}
	var passed []passedLanding
	if req.parentRunID.IsZero() && def.Spec.Role == "landing" {
		occupied, err := queries.HasActiveLanding(ctx, workflowsql.HasActiveLandingParams{ProjectID: req.ProjectID, Status: RunRunning.String()})
		if err != nil {
			return nil, err
		}
		if occupied {
			return nil, landingOccupied{}
		}
		if passed, err = mergedActiveLandingsIn(ctx, queries, req.ProjectID); err != nil {
			return nil, err
		}
	}
	first := def.Spec.Stages[0].Name
	at := s.now() // AppTown's time the run was made
	number, err := queries.NextRunNumber(ctx, req.ProjectID)
	if err != nil {
		return nil, err
	}
	inserted, err := queries.InsertRun(ctx, workflowsql.InsertRunParams{ID: ids.New(at), Number: number, ProjectID: req.ProjectID, WorkflowName: def.Workflow.Name,
		WorkflowVersion: int64(def.Workflow.Version), WorkflowOrigin: def.Workflow.Origin, Kind: def.Spec.Kind, Status: RunRunning.String(),
		CurrentStage: first, DefinitionJson: definition, ChangeID: req.ChangeID, ParentRunID: req.parentRunID, ParentStepID: req.parentStepID,
		InputsJson: json.RawMessage(req.InputsJSON), AdoptionID: def.AdoptionID, CreatedAt: at, UpdatedAt: at})
	if err != nil {
		return nil, err
	}
	id := inserted.ID
	if len(passed) > 0 {
		if err := recordReceiptIn(ctx, tx, id, "landing.passed", landingPassedReceipt{Landings: passed}, at); err != nil {
			return nil, err
		}
	}
	if err := markLandingAuthorityIn(ctx, tx, id, req.landingBy); err != nil {
		return nil, err
	}
	// The owner is the requester, resolved at the start, or the parent's
	// owner as the parent recorded them.
	owner := req.owner
	if owner.Name == "" {
		var err error
		if owner, err = identity.ActorIn(ctx, tx, req.requestedBy); err != nil {
			return nil, err
		}
	}
	parent := req.parentRunID
	if parent.IsZero() {
		parent = req.acceptedRunID
	}
	if !parent.IsZero() {
		inference, err := queries.RunInferenceOwnerByID(ctx, parent)
		if errors.Is(err, sql.ErrNoRows) {
			owner = access.Actor{Name: fmt.Sprintf("run/%d", parent)}
		} else if err != nil {
			return nil, err
		} else {
			owner = access.Actor{Name: inference.Actor, Principal: inference.ActorPrincipal, Via: inference.ActorVia}
		}
	}
	if owner.Name == "" {
		owner = access.Actor{Name: "system"}
	}
	if _, err := queries.InsertRunInferenceOwner(ctx, workflowsql.InsertRunInferenceOwnerParams{RunID: id, Actor: owner.Name, ActorPrincipal: owner.Principal, ActorVia: owner.Via}); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = requireRunWrite(0, nil)
		}
		return nil, err
	}
	if _, err := queries.InsertRunStart(ctx, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = requireRunWrite(0, nil)
		}
		return nil, err
	}
	if req.parentRunID.IsZero() && !req.ChangeID.IsZero() {
		act := attachChangeRun
		if def.Spec.Role == "landing" {
			act = LandChange
		}
		if _, err := TransitionChangeIn(ctx, tx, req.ProjectID, req.ChangeID, ChangeMove{Action: act, RunID: id}, at); err != nil {
			return nil, err
		}
	}
	// A writing root run takes its change: it holds the branch from now on,
	// and is refused while a chat, another run or a desktop checkout holds
	// it. A landing takes nothing and only reads; it is refused while a run
	// or a checkout holds the change, and runs beside the change's chat.
	if req.parentRunID.IsZero() {
		if def.Spec.Role == "landing" {
			if err := landBesideHolderIn(ctx, tx, RunRef{ID: id, ProjectID: req.ProjectID}, s.chats, s.now()); err != nil {
				return nil, err
			}
		} else if err := takeChangeForRunIn(ctx, tx, RunRef{ID: id, ProjectID: req.ProjectID}, s.chats, s.now()); err != nil {
			return nil, err
		}
	}
	if err := recordReceiptIn(ctx, tx, id, "run.created", runCreatedReceipt{def.Workflow.Name, def.Workflow.Version, def.Workflow.Origin}, at); err != nil {
		return nil, err
	}
	for _, event := range def.Events {
		if err := recordEventIn(ctx, tx, id, event.Kind, event.Payload, at); err != nil {
			return nil, err
		}
	}
	if _, err := s.stages.EnterIn(ctx, tx, id, &def.Spec, first, at); err != nil {
		return nil, err
	}
	if req.parentRunID.IsZero() && !req.ChangeID.IsZero() && def.Spec.Role == "landing" {
		if err := recordReceiptIn(ctx, tx, id, "change.landing", changeLandingReceipt{req.ChangeID}, at); err != nil {
			return nil, err
		}
	}
	if !req.acceptedRunID.IsZero() {
		if err := requireRunWrite(queries.AttachAcceptedLanding(ctx, workflowsql.AttachAcceptedLandingParams{LandingRunID: id, WorkRunID: req.acceptedRunID})); err != nil {
			return nil, err
		}
	}
	if !req.parentStepID.IsZero() {
		if err := requireRunWrite(queries.SetParentStepAttempt(ctx, workflowsql.SetParentStepAttemptParams{ParentStepAttempt: req.parentStepAttempt + 1, ID: id})); err != nil {
			return nil, err
		}
		evidence := calledRunEvidence{id, number, def.Workflow.Name, def.Workflow.Version, at.UnixMilli()}
		evidenceJSON, err := json.Marshal(evidence)
		if err != nil {
			return nil, err
		}
		if err := requireAttemptWrite(queries.StartParentCall(ctx, workflowsql.StartParentCallParams{Status: StatusRunning.String(), StartedAt: sql.NullTime{Time: at, Valid: true}, EvidenceJson: evidenceJSON, ID: req.parentStepID, OldStatus: StatusPending.String(), Attempt: req.parentStepAttempt})); err != nil {
			return nil, err
		}
		if err := recordReceiptIn(ctx, tx, req.parentRunID, "step.started", stepStartedReceipt{Step: parentStepName, Stage: parentStage, Type: "workflow/call", Attempt: req.parentStepAttempt + 1}, at); err != nil {
			return nil, err
		}
		if err := recordReceiptIn(ctx, tx, req.parentRunID, "run.called", runCalledReceipt{parentStepName, def.Workflow.Name, def.Workflow.Version, id}, at); err != nil {
			return nil, err
		}
	}
	return readRun(ctx, tx, id)
}

func (s runStartStore) pendingPreparation(ctx context.Context, id ids.ID) (*Run, error) {
	run, err := readRun(ctx, s.db, id)
	if err != nil {
		return nil, err
	}
	if run.Status != RunRunning {
		return nil, nil
	}
	pending, err := runNeedsPreparation(ctx, s.db, id)
	if err != nil {
		return nil, err
	}
	if !pending {
		return nil, nil
	}
	return run, nil
}
func (s runStartStore) prepared(ctx context.Context, id ids.ID) error {
	return s.db.InTx(ctx, func(tx *database.Tx) error {
		n, err := workflowsql.New(tx.SQL()).MarkRunReady(ctx, id)
		if err != nil {
			return err
		}
		if n == 0 {
			// Zero writes is a replay only if readiness really is persisted. A
			// missing row or a vetoed update must not authorize external dispatch.
			start, err := workflowsql.New(tx.SQL()).RunStartByID(ctx, id)
			if err != nil {
				return err
			}
			if !start.Ready {
				return fmt.Errorf("run %d readiness was not recorded", id)
			}
			return nil
		}
		if err := requireRunWrite(n, nil); err != nil {
			return err
		}
		return recordReceiptIn(ctx, tx, id, "run.ready", struct{}{}, s.now())
	})
}

func (s runStartStore) preparationFailed(ctx context.Context, id ids.ID, message string) error {
	return s.db.InTx(ctx, func(tx *database.Tx) error {
		return recordReceiptIn(ctx, tx, id, "branch.error", struct {
			Error string `json:"error"`
		}{message}, s.now())
	})
}
