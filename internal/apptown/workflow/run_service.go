package workflow

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/adoption"
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
	if err := s.touchChange(ctx, parent.ProjectID, parent.ChangeID); err != nil {
		return nil, err
	}
	var condition struct {
		WhenFailed []string `json:"whenFailed"`
		Findings   string   `json:"findings"`
	}
	if err := DecodeStepConfig(step.Config, &condition); err != nil {
		return nil, err
	}
	if len(condition.WhenFailed) > 0 {
		if condition.Findings != "fixable" {
			return nil, fmt.Errorf("conditional call needs findings: fixable")
		}
		if s.fixes == nil {
			return nil, fmt.Errorf("fix declarations are unavailable")
		}
		revision, err := adoption.ByIDIn(ctx, s.db, parent.AdoptionID)
		if err != nil {
			return nil, err
		}
		ds, err := s.fixes.Fixes(ctx, adoption.Source{ProjectID: parent.ProjectID, Revision: revision})
		if err != nil {
			return nil, err
		}
		execution := &Execution{service: s, ctx: ctx, run: parent, step: step}
		targets, why, err := execution.fixableTargets(ctx, condition.WhenFailed, ds)
		if err != nil {
			return nil, err
		}
		if len(targets) == 0 {
			claim, err := s.stepAttempts.start(ctx, step, "workflow/call", ids.Nil)
			if err != nil {
				return nil, err
			}
			outcome, err := EncodeOutcome(StatusPass, map[string]any{"notNeeded": true, "reason": why}, nil)
			if err != nil {
				return nil, err
			}
			return nil, s.finishAttempt(ctx, claim, outcome)
		}
		var inputs map[string]json.RawMessage
		if err := json.Unmarshal(in.Inputs, &inputs); err != nil {
			return nil, err
		}
		if inputs == nil {
			inputs = map[string]json.RawMessage{}
		}
		raw, err := json.Marshal(targets)
		if err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(string(raw))
		if err != nil {
			return nil, err
		}
		inputs["targets"] = encoded
		in.Inputs, err = json.Marshal(inputs)
		if err != nil {
			return nil, err
		}
	}
	return s.starter.start(ctx, StartRequest{
		ProjectID: parent.ProjectID, ChangeID: parent.ChangeID, WorkflowName: in.Workflow, InputsJSON: string(in.Inputs),
		parentRunID: parent.ID, parentStepID: step.ID, parentStepAttempt: step.Attempt, parentStepConfig: string(step.Config),
		requestedBy: actor.InferenceOwner, owner: actor.Owner, receiptActor: actor.Receipt,
	})
}

func (s *Service) RequestLanding(ctx context.Context, changeID ids.ID) (*Run, error) {
	return s.startChangeRun(ctx, StartChangeRun{ChangeID: changeID, Workflow: "land"}, actionRequestLanding)
}

// RestartLanding starts the same workflow as an abandoned root landing. The
// workflow name comes from the stored run, never from a caller's choice.
// The caller validates current rules before abandoning the previous run.
func (s *Service) RestartLanding(ctx context.Context, runID ids.ID) (*Run, error) {
	if _, _, err := s.runScope(ctx, runID, actionRequestLanding); err != nil {
		return nil, err
	}
	run, err := readRun(ctx, s.db, runID)
	if err != nil {
		return nil, err
	}
	spec, err := run.Definition()
	if err != nil {
		return nil, err
	}
	if run.Status != RunAbandoned || !run.ParentRunID.IsZero() || run.ChangeID.IsZero() || spec.Role != "landing" {
		return nil, fmt.Errorf("only an abandoned landing on a change can be restarted")
	}
	return s.startChangeRun(ctx, StartChangeRun{ChangeID: run.ChangeID, Workflow: run.WorkflowName}, actionRequestLanding)
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
	if err := s.touchChange(ctx, project, change.ID); err != nil {
		return nil, err
	}
	return s.starter.start(ctx, StartRequest{requestLanding: action == actionRequestLanding, ProjectID: project, ChangeID: change.ID, WorkflowName: in.Workflow, InputsJSON: string(in.Inputs), requestedBy: actor.InferenceOwner, owner: actor.Owner, receiptActor: actor.Receipt})
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
	return s.runAdmissions.createIn(ctx, tx, in, def)
}
