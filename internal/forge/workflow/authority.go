package workflow

import (
	"context"
	"errors"
	"fmt"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
)

// commandAction selects an internal authorization rule. Callers submit commands,
// not authorization actions.
type commandAction uint8

const (
	actionIdentify commandAction = iota + 1
	actionReadRun
	actionReadProject
	actionReadEvidence
	actionAttachEvidence
	actionReadChange
	actionListChanges
	actionCreateChange
	actionEditBrief
	// actionPublishNarrative writes the change's So far summary, not its brief.
	actionPublishNarrative
	actionBeginChange
	actionImportChange
	actionOpenUplift
	actionStartRun
	actionRequestLanding
	// actionLandUnassented starts a run whose definition would merge, deploy
	// or call another workflow before any person's approval (assent.go).
	actionLandUnassented
	actionStopRun
	// actionInterruptStep stops a running agent step: working on the change.
	actionInterruptStep
	actionCloseChange
	actionRetryAttempt
	actionSendBackChange
	actionApproveStep
	actionOverrideStep
	actionAdvanceRun
	actionRunStep // the engine records a step's attempt, re-checks an assertion, redispatches a stage
)

type operationPermission struct {
	StepID                     ids.ID
	Approvers                  []string
	Action                     commandAction
	ProjectID, ChangeID, RunID ids.ID
}

// commandActor is verified attribution. Receipt labels may include an instrument;
// inference ownership identifies the principal paying for the operation.
type commandActor struct {
	Receipt, InferenceOwner string
	// Owner is the inference owner as the caller was admitted: their
	// principal and way (a chat's opener and the chat). Empty when the
	// caller carried no principal and is not a chat.
	Owner access.Actor
}

type operationAuthority interface {
	Require(context.Context, operationPermission) (commandActor, error)
	VisibleProjects(context.Context, commandAction) ([]ids.ID, error)
}

var ErrCallerRequired = fault.New(fault.Unauthenticated, "Work operations require explicit caller authority")

// commandAuthority owns Work eligibility and operation-to-capability mapping.
// The injected kernel observes the caller's current grants and credential bounds.
type commandAuthority struct {
	db         *database.DB
	checked    access.Authorizer
	visibility access.ReadAuthorizer
	clocked
}

func (w commandAuthority) Require(ctx context.Context, p operationPermission) (commandActor, error) {
	if w.checked == nil {
		return commandActor{}, ErrCallerRequired
	}
	c, _ := access.CallerFrom(ctx)
	pr := c.Principal
	if pr.Name == "" || (pr.Kind != "user" && pr.Kind != "instrument" && pr.Kind != "anonymous") {
		return commandActor{}, ErrCallerRequired
	}
	if err := ctx.Err(); err != nil {
		return commandActor{}, err
	}
	// Identify names the caller; every operation asks again for its action,
	// and that is where a disabled or unverified account is refused.
	if p.Action == actionIdentify {
		return callerActor(c), nil
	}
	if !c.Verified {
		return commandActor{}, ErrCallerRequired
	}
	// Step decisions answer to the approval policy alone, which carries its
	// own account and scope checks and its own denial codes.
	if p.Action == actionApproveStep || p.Action == actionOverrideStep {
		who, err := w.requireDecision(ctx, pr, p)
		// An approval is a person's own assent: given directly, in
		// a session, through a trusted header, in open mode or with their
		// command-line token; never through a chat, a run step, an agent's
		// connection or a ticket, though those act for the same person.
		if actor := pr.Actor(); err == nil && p.Action == actionApproveStep && !actor.Assents() {
			err = &DecisionDenied{Code: access.DenyDelegationNarrow, Reason: fmt.Sprintf("an approval is a person's own decision, given in their browser or with their command-line token, not %s", wayWords(actor))}
		}
		return commandActor{Receipt: who}, err
	}
	deny := func(code, reason string) (commandActor, error) {
		return commandActor{}, &access.Denied{Code: code, Reason: reason}
	}
	if err := identity.RequireEnabledIn(ctx, w.db, pr); err != nil {
		return commandActor{}, err
	}
	capability, ok := operationCapability(p.Action)
	if !ok {
		return deny(access.DenyOutsideScope, "unknown Work operation")
	}
	// An instrument acts in its one project; AppTown's own engine, with no
	// project of its own, acts in any.
	if pr.Kind == "instrument" {
		if !pr.ProjectID.IsZero() && p.ProjectID != pr.ProjectID {
			return deny(access.DenyOutsideScope, "this session may access only its project's work")
		}
		if pr.ProjectID.IsZero() && pr.Instrument != "forge" {
			return deny(access.DenyOutsideScope, "this session may access only its project's work")
		}
	}

	resource := access.Resource{Type: "instance"}
	if !p.ProjectID.IsZero() {
		resource = access.Resource{Type: "project", ID: p.ProjectID.String(), ProjectID: p.ProjectID, ChangeID: p.ChangeID, RunID: p.RunID}
	}
	_, err := w.checked.Require(ctx, access.Permission{Capability: capability, Resource: resource, Context: access.Context{Route: "work"}})
	if err != nil {
		return commandActor{}, err
	}
	return callerActor(c), nil
}

func callerActor(c access.Caller) commandActor {
	receipt := c.Receipt
	if receipt == "" {
		receipt = c.Principal.Name
	}
	owner := c.Principal.Person
	if owner == "" {
		owner = c.Principal.Name
	}
	out := commandActor{Receipt: receipt, InferenceOwner: owner}
	// A chat's owner is its opener as the chat recorded them, unresolved if
	// it recorded none; a person's is the admitted principal.
	if !c.Principal.ID.IsZero() || c.Principal.Instrument == "console" {
		out.Owner = c.Principal.Actor()
		out.Owner.Name = owner
	}
	return out
}

func (w commandAuthority) requireDecision(ctx context.Context, pr access.Principal, request operationPermission) (string, error) {
	if pr.Name == "anonymous" || (pr.Kind != "user" && pr.Kind != "instrument") {
		return "", ErrCallerRequired
	}
	scope := request
	capability := access.CapChangeOverride
	switch request.Action {
	case actionOverrideStep:
	case actionApproveStep:
		capability = access.CapChangeApprove
	default:
		return "", ErrStepDecisionCaller
	}
	person := pr.Name
	if pr.Instrument == "console" {
		person = pr.Person
	}
	if err := identity.RequireEnabledIn(ctx, w.db, pr); err != nil {
		var denied *access.Denied
		if errors.As(err, &denied) {
			return "", &DecisionDenied{Code: denied.Code, Reason: denied.Reason}
		}
		return "", err
	}
	if request.Action == actionApproveStep {
		voter := pr.ID
		if voter.IsZero() {
			resolved, err := identity.ActorIn(ctx, w.db, person)
			if err != nil {
				return "", err
			}
			voter = resolved.Principal
		}
		if err := requireApprover(ctx, w.db, scope, request.Approvers, person, voter, w.now()); err != nil {
			return "", err
		}
	}
	resource := access.Resource{Type: "step", ID: scope.StepID.String(), ProjectID: scope.ProjectID, ChangeID: scope.ChangeID, RunID: scope.RunID}
	_, err := w.checked.Require(ctx, access.Permission{Capability: capability, Resource: resource})
	if err != nil {
		var denied *access.Denied
		if errors.As(err, &denied) {
			return "", &DecisionDenied{Code: denied.Code, Reason: denied.Reason}
		}
		return "", err
	}
	return pr.Name, nil
}

// wayWords says how an actor acted, for a refusal.
func wayWords(a access.Actor) string {
	switch a.Way() {
	case access.ViaChat:
		return "through a chat"
	case access.ViaRunStep:
		return "from a workflow step"
	case access.ViaTokenAgent:
		return "through a coding agent's token"
	case access.ViaOAuth:
		return "through a connected agent"
	case access.ViaTicket:
		return "with a ticket"
	}
	if a.Principal.IsZero() {
		return "by a name not resolved to a person"
	}
	return "this way (" + a.Via + ")"
}

func (s *Service) require(ctx context.Context, p operationPermission) (string, error) {
	actor, err := s.requireActor(ctx, p)
	return actor.Receipt, err
}

func (s *Service) requireActor(ctx context.Context, p operationPermission) (commandActor, error) {
	if s == nil {
		return commandActor{}, ErrCallerRequired
	}
	return requireActor(ctx, s.authority, p)
}
func requireActor(ctx context.Context, authority operationAuthority, p operationPermission) (commandActor, error) {
	if err := ctx.Err(); err != nil {
		return commandActor{}, err
	}
	if authority == nil {
		return commandActor{}, ErrCallerRequired
	}
	actor, err := authority.Require(ctx, p)
	if err == nil && actor.Receipt == "" {
		err = ErrCallerRequired
	}
	return actor, err
}

func (s *Service) runScope(ctx context.Context, id ids.ID, act commandAction) (ids.ID, commandActor, error) {
	if _, err := s.require(ctx, operationPermission{Action: actionIdentify}); err != nil {
		return ids.Nil, commandActor{}, err
	}
	project, err := runProject(ctx, s.db, id)
	if err != nil {
		return ids.Nil, commandActor{}, err
	}
	actor, err := s.requireActor(ctx, operationPermission{Action: act, ProjectID: project, RunID: id})
	return project, actor, err
}

// stepAct is runScope for a step: the run it belongs to decides the scope.
func (s *Service) stepAct(ctx context.Context, stepID ids.ID, act commandAction) (commandActor, error) {
	if _, err := s.require(ctx, operationPermission{Action: actionIdentify}); err != nil {
		return commandActor{}, err
	}
	runID, err := workflowsql.New(s.db.SQL()).StepRunIDMeta(ctx, stepID)
	if err != nil {
		return commandActor{}, database.Missing(err)
	}
	_, actor, err := s.runScope(ctx, runID, act)
	return actor, err
}

func operationCapability(action commandAction) (access.Capability, bool) {
	capability, ok := map[commandAction]access.Capability{actionReadProject: access.CapProjectRead, actionReadRun: access.CapRunRead, actionReadEvidence: access.CapEvidenceRead, actionAttachEvidence: access.CapEvidenceAttach, actionReadChange: access.CapChangeRead, actionListChanges: access.CapChangeList, actionCreateChange: access.CapChangeCreate, actionEditBrief: access.CapChangeUpdateBrief, actionPublishNarrative: access.CapChangeNarrative, actionBeginChange: access.CapChangeInvokeLane, actionImportChange: access.CapProjectImport, actionOpenUplift: access.CapProjectConfigure, actionStartRun: access.CapChangeInvokeLane, actionRequestLanding: access.CapChangeRequestLand, actionLandUnassented: access.CapChangeLand, actionStopRun: access.CapRunOperate, actionInterruptStep: access.CapChangeInvokeLane, actionRetryAttempt: access.CapRunOperate, actionCloseChange: access.CapChangeAbandon}[action]
	if action == actionSendBackChange {
		capability, ok = access.CapChangeSendBack, true
	}
	if action == actionAdvanceRun || action == actionRunStep {
		capability, ok = access.CapRunOperate, true
	}
	return capability, ok
}
func (w commandAuthority) VisibleProjects(ctx context.Context, action commandAction) ([]ids.ID, error) {
	if w.checked == nil {
		return nil, ErrCallerRequired
	}
	capability, ok := operationCapability(action)
	if !ok {
		return nil, &access.Denied{Code: access.DenyOutsideScope, Reason: "unknown Work operation"}
	}
	if w.visibility == nil {
		return nil, access.ErrAuthorityUnavailable
	}
	return w.visibility.VisibleProjects(ctx, capability)
}
