package workflow

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/database"
)

// StartChangeRun describes an explicit root invocation. Parent/acceptance IDs
// and billing identity cannot be supplied through the caller-facing command.
type StartChangeRun struct {
	ChangeID ids.ID
	Workflow string
	Inputs   json.RawMessage
}

func (s *Service) StartRun(ctx context.Context, in StartChangeRun) (*Run, error) {
	return s.startChangeRun(ctx, in, actionStartRun)
}

// WorkflowCall is a step calling another workflow: the child is a real run
// in the parent's story, pinned to the step that called it. The caller is
// the parent run itself, acting as its step agent's token would.
type WorkflowCall struct {
	ParentStepID ids.ID
	Workflow     string
	Inputs       json.RawMessage
}

func (s *Service) Call(ctx context.Context, in WorkflowCall) (*Run, error) {
	if _, err := s.require(ctx, operationPermission{Action: actionIdentify}); err != nil {
		return nil, err
	}
	step, err := readStep(ctx, s.db, in.ParentStepID)
	if err != nil {
		return nil, err
	}
	parent, err := readRun(ctx, s.db, step.RunID)
	if err != nil {
		return nil, err
	}
	return s.callFrom(ctx, parent, step, in)
}

// Call starts the child against this execution's original parent/attempt/config.
// The child admission transaction owns the call step's claim and eventual result.
func (e *Execution) Call(ctx context.Context, name string, inputs json.RawMessage) (*Run, error) {
	return e.service.callFrom(ctx, e.run, e.step, WorkflowCall{ParentStepID: e.step.ID, Workflow: name, Inputs: inputs})
}

func (s *Service) callFrom(ctx context.Context, parent *Run, step *Step, in WorkflowCall) (*Run, error) {
	actor, err := s.requireActor(ctx, operationPermission{Action: actionStartRun, ProjectID: parent.ProjectID, ChangeID: parent.ChangeID, RunID: parent.ID})
	if err != nil {
		return nil, err
	}
	if s.starter == nil {
		return nil, fmt.Errorf("Run execution is unavailable")
	}
	return s.starter.Start(ctx, StartRequest{
		ProjectID: parent.ProjectID, ChangeID: parent.ChangeID, WorkflowName: in.Workflow, InputsJSON: string(in.Inputs),
		parentRunID: parent.ID, parentStepID: step.ID, parentStepAttempt: step.Attempt, parentStepConfig: string(step.Config),
		requestedBy: actor.InferenceOwner, owner: actor.Owner, receiptActor: actor.Receipt,
	})
}

func (s *Service) RequestLanding(ctx context.Context, changeID ids.ID) (*Run, error) {
	return s.startChangeRun(ctx, StartChangeRun{ChangeID: changeID, Workflow: "land"}, actionRequestLanding)
}

func (s *Service) startChangeRun(ctx context.Context, in StartChangeRun, action commandAction) (*Run, error) {
	project, actor, err := s.changeScope(ctx, in.ChangeID, action)
	if err != nil {
		return nil, err
	}
	if actor.InferenceOwner == "" {
		return nil, ErrCallerRequired
	}
	change, err := readChangeInProject(ctx, s.db, in.ChangeID, project)
	if err != nil {
		return nil, err
	}
	if action == actionRequestLanding && change.State != ChangeInProgress {
		return nil, fmt.Errorf("change #%d is %s — only in-progress changes land", change.ID, change.State)
	}
	if s.starter == nil {
		return nil, fmt.Errorf("Run execution is unavailable")
	}
	return s.starter.Start(ctx, StartRequest{ProjectID: project, ChangeID: change.ID, WorkflowName: in.Workflow, InputsJSON: string(in.Inputs), requestedBy: actor.InferenceOwner, owner: actor.Owner, receiptActor: actor.Receipt})
}

// StartSource says what a start of this request would start from: its
// change, the adoption in force, and the failed landings it would replace.
// Remediation resolves a definition against it before admitting the run.
func (s *Service) StartSource(ctx context.Context, in StartRequest) (StartSource, error) {
	if _, err := s.require(ctx, operationPermission{Action: actionIdentify}); err != nil {
		return StartSource{}, err
	}
	if _, err := s.requireActor(ctx, operationPermission{Action: actionReadChange, ProjectID: in.ProjectID, ChangeID: in.ChangeID}); err != nil {
		return StartSource{}, err
	}
	return s.runAdmissions.Source(ctx, in)
}

// AdmitRunIn joins an already-authorized operation in the caller's transaction.
// It supplies verified attribution; the caller owns authorization and commit.
// The caller dispatches the admitted run only after a successful commit.
func (s *Service) AdmitRunIn(ctx context.Context, tx *database.Tx, in StartRequest, def PreparedRun) (*Run, error) {
	actor, err := s.requireActor(ctx, operationPermission{Action: actionIdentify})
	if err != nil {
		return nil, err
	}
	in.requestedBy, in.owner, in.receiptActor = actor.InferenceOwner, actor.Owner, actor.Receipt
	in.landingBy = s.landingBy(ctx, in)
	return s.runAdmissions.CreateIn(ctx, tx, in, def)
}
