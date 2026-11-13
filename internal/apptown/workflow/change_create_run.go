package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

// InvokeChange admits a new Change and its first Run as one operation.
// Existing changes start runs through StartRun instead.
type InvokeChange struct {
	Change   ChangeDraft
	Workflow string
	Inputs   json.RawMessage
	// join, when set, records another owner's facts about the new Change in
	// the admission transaction.
	join            admissionParticipant
	integrationWork bool
}

// admissionParticipant runs inside a new Change's admission transaction, after
// the Change (and, for an invocation, its first Run) is written and before
// anything commits, prepares a workspace or dispatches. An error rolls back the
// whole admission. A participant writes only its own owner's tables through tx
// and makes no git, Docker or provider call: nothing outside the database can be
// undone by the rollback. Integration wiring links its attachment this way.
type admissionParticipant func(ctx context.Context, tx *database.Tx, ch *Change) error

const invocationSettlementTimeout = 5 * time.Second
const preparedChangeReadTimeout = 5 * time.Second

// CreateIntegrationWiring admits the Change and AppTown's integration wiring
// workflow together. Callers provide the Change and transactional attachment
// link; the workflow identity belongs to AppTown. Authority, preparation and
// failure settlement follow CreateAndRun.
func (s *Service) CreateIntegrationWiring(ctx context.Context, draft ChangeDraft, join admissionParticipant) (*Change, *Run, error) {
	return s.CreateAndRun(ctx, InvokeChange{Change: draft, Workflow: "adopt-integration", join: join, integrationWork: true})
}

// CreateIntegrationRetirement admits the Change and AppTown's integration
// retirement workflow together. Callers cannot choose the workflow identity.
// Admission and failure settlement follow CreateAndRun. The participant records
// the integration owner's facts in the admission transaction.
func (s *Service) CreateIntegrationRetirement(ctx context.Context, draft ChangeDraft, join admissionParticipant) (*Change, *Run, error) {
	return s.CreateAndRun(ctx, InvokeChange{Change: draft, Workflow: "retire-integration", join: join, integrationWork: true})
}

// MayOpenWork checks that the caller may open a Change and start its run in
// the project, the two acts CreateAndRun requires of a person, and returns
// the caller's receipt. Asking records nothing; a refusal is the error. A
// schedule fired at a person's request checks this before acting as the
// schedule.
func (s *Service) MayOpenWork(ctx context.Context, projectID ids.ID) (string, error) {
	if _, err := s.requireActor(ctx, operationPermission{Action: actionCreateChange, ProjectID: projectID}); err != nil {
		return "", err
	}
	return s.require(ctx, operationPermission{Action: actionStartRun, ProjectID: projectID})
}

// CreateAndRun composes governed Change creation and Run admission.
// Nothing is committed unless both identities and the first stage are admitted.
func (s *Service) CreateAndRun(ctx context.Context, in InvokeChange) (*Change, *Run, error) {
	if in.Change.TitleFromChat {
		return nil, nil, fmt.Errorf("an invocation needs an active Change, not a chat draft")
	}
	if _, err := s.requireActor(ctx, operationPermission{Action: actionCreateChange, ProjectID: in.Change.ProjectID}); err != nil {
		return nil, nil, err
	}
	actor, err := s.requireActor(ctx, operationPermission{Action: actionStartRun, ProjectID: in.Change.ProjectID})
	if err != nil {
		return nil, nil, err
	}
	if actor.InferenceOwner == "" {
		return nil, nil, ErrCallerRequired
	}
	if s.starter == nil {
		return nil, nil, fmt.Errorf("Run execution is unavailable")
	}
	req := StartRequest{integrationWork: in.integrationWork, ProjectID: in.Change.ProjectID, WorkflowName: in.Workflow, InputsJSON: string(in.Inputs), requestedBy: actor.InferenceOwner, owner: actor.Owner, receiptActor: actor.Receipt}
	if req.InputsJSON == "" {
		req.InputsJSON = "{}"
	}
	if err := validateStart(req); err != nil {
		return nil, nil, err
	}
	source, err := s.runAdmissions.Source(ctx, req)
	if err != nil {
		return nil, nil, err
	}
	def, err := s.starter.definition(ctx, req, source)
	if err != nil {
		return nil, nil, err
	}
	var item *Change
	run, err := database.Transact(ctx, s.db, func(tx *database.Tx) (*Run, error) {
		created, err := CreateChangeIn(ctx, tx, in.Change, actor.Receipt, s.now())
		if err != nil {
			return nil, err
		}
		req.ChangeID = created.Change.ID
		admitted, err := s.AdmitRunIn(ctx, tx, req, def)
		if err != nil {
			return nil, err
		}
		if item, err = readChange(ctx, tx, req.ChangeID); err != nil {
			return nil, err
		}
		if in.join != nil {
			if err := in.join(ctx, tx, item); err != nil {
				return nil, err
			}
		}
		return admitted, nil
	})
	if err != nil {
		return nil, nil, err
	}
	if err := s.starter.prepare(ctx, run.ID); err != nil {
		return item, run, s.settleInvocation(ctx, item, run, actor.Receipt, err)
	}
	// Readiness is committed. Refresh the response independently of the caller,
	// with a short bound so a disconnected request cannot hold this read open.
	readCtx, cancelRead := context.WithTimeout(context.WithoutCancel(ctx), preparedChangeReadTimeout)
	prepared, err := readChange(readCtx, s.db, item.ID)
	cancelRead()
	if err != nil {
		return item, run, fmt.Errorf("change %d run %d ready; reading prepared change: %w", item.ID, run.ID, err)
	}
	item = prepared
	if err := s.starter.dispatchPrepared(ctx, run); err != nil {
		return item, run, fmt.Errorf("change %d run %d admitted; inspect or retry this run: %w", item.ID, run.ID, err)
	}
	// Return committed admission facts. A post-dispatch read failure must not
	// turn successful handoff into an invitation to create a duplicate.
	return item, run, nil
}

// Settlement is the command's compensation, not another user permission. It
// can close only its still-unready run; readiness permits concurrent dispatch.
func (s *Service) settleInvocation(ctx context.Context, item *Change, run *Run, who string, cause error) error {
	ctx, cancel := invocationSettlementContext(ctx)
	defer cancel()
	closure, lockErr := scm.PrepareClosure(ctx, s.abandonments.locks, item.ProjectID)
	if lockErr != nil {
		return errors.Join(cause, fmt.Errorf("settlement repository lock: %w", lockErr))
	}
	defer closure.Close()
	err := s.db.InTx(ctx, func(tx *database.Tx) error {
		q := workflowsql.New(tx.SQL())
		if _, err := q.ProjectForUpdate(ctx, item.ProjectID); err != nil {
			return err
		}
		ch, err := q.ChangeForUpdate(ctx, workflowsql.ChangeForUpdateParams{ID: item.ID, ProjectID: item.ProjectID})
		if err != nil {
			return err
		}
		current, err := q.RunForUpdate(ctx, run.ID)
		if err != nil {
			return err
		}
		start, err := q.RunStartByID(ctx, run.ID)
		if err != nil {
			return err
		}
		if start.Ready || ch.RunID != run.ID || current.ProjectID != item.ProjectID || current.ChangeID != item.ID || current.Status != RunRunning.String() {
			return fmt.Errorf("admission is ready or has changed; inspect it before abandoning")
		}
		_, err = s.abandonments.abandonIn(ctx, tx, Abandonment{ChangeID: item.ID, Who: who, Reason: cause.Error()}, item.ProjectID, closure)
		return err
	})
	if err != nil {
		return errors.Join(fmt.Errorf("change %d run %d preparation failed: %w", item.ID, run.ID, cause), fmt.Errorf("settlement failed: %w", err))
	}
	return fmt.Errorf("change %d run %d abandoned after preparation failed: %w", item.ID, run.ID, cause)
}

func invocationSettlementContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), invocationSettlementTimeout)
}
