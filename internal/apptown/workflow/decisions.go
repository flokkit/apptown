package workflow

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/fault"
)

var (
	errStepDecisionCaller   = fault.New(fault.Unauthenticated, "a verified caller is required for a step decision")
	errStepDecisionConflict = fault.New(fault.Conflict, "step changed or is not awaiting a decision in the active run stage")
	errOverrideReason       = fault.New(fault.Invalid, "an override requires a justification")
	errStepApprovalInput    = fault.New(fault.Invalid, "invalid approval request")
)

// approvalTooEarly refuses an approval while its stage's work is still to
// finish; it can be given once those steps have.
type approvalTooEarly struct{ Unfinished []string }

func (e *approvalTooEarly) Error() string {
	return "the stage's work has not finished (" + strings.Join(e.Unfinished, ", ") + "); approve once it has"
}
func (e *approvalTooEarly) FaultKind() fault.Kind { return fault.Conflict }
func (e *approvalTooEarly) FaultCode() string     { return "stage-work-unfinished" }

type DecisionDenied struct{ Code, Reason string }

func (e *DecisionDenied) Error() string         { return e.Reason }
func (e *DecisionDenied) FaultKind() fault.Kind { return fault.Forbidden }
func (e *DecisionDenied) FaultCode() string     { return e.Code }

type decisionScope struct{ StepID, RunID, ProjectID, ChangeID ids.ID }
type Override struct {
	StepID ids.ID
	Reason string
}
type Approval struct {
	StepID   ids.ID
	Decision ApprovalDecision
	Comment  string
	// Shown is what the client displayed of the landing's approvals beside
	// this acceptance (ApprovalQuestion). The vote records them only if they
	// are still what the landing asks; nil records nothing, so the acceptance
	// will not count for the landing.
	Shown []string
	// Adds is the digest of what the change adds as the client showed it
	// (ApprovalQuestion.Adds). An approving vote on an approval that assents
	// to a commit must send it, and it must still be the list.
	Adds string
}

// errLandingQuestionChanged refuses an acceptance whose displayed landing
// sentences are no longer what the landing asks: the person agrees only to
// words they read. It carries a code so a client can show the new words.
var errLandingQuestionChanged error = landingQuestionChanged{}

type landingQuestionChanged struct{}

func (landingQuestionChanged) Error() string {
	return "what approving this would also approve for the landing has changed since it was shown; read it again and decide"
}
func (landingQuestionChanged) FaultKind() fault.Kind { return fault.Conflict }
func (landingQuestionChanged) FaultCode() string     { return "question-changed" }

type overrideReceipt struct {
	Step    string `json:"step"`
	By      string `json:"by"`
	Reason  string `json:"reason"`
	Attempt int64  `json:"attempt"`
}

// Every caller-facing decision starts with identification, then rechecks the
// authorized scope/configuration/attempt/revision under the project's locks.
func (s *Service) prepareDecision(ctx context.Context, id ids.ID, action commandAction) (stepDecisionSnapshot, string, error) {
	if _, err := s.require(ctx, operationPermission{Action: actionIdentify}); err != nil {
		return stepDecisionSnapshot{}, "", err
	}
	expected, err := (stepDecisionStore{s.db, s.clocked}).prepare(ctx, id)
	if err != nil {
		return expected, "", err
	}
	permission := operationPermission{Action: actionOverrideStep, ProjectID: expected.scope.ProjectID, ChangeID: expected.scope.ChangeID, RunID: expected.scope.RunID, StepID: expected.scope.StepID}
	if action == actionApproveStep {
		if expected.stepType != "approval" {
			return expected, "", fmt.Errorf("%w: step is not an approval", errStepApprovalInput)
		}
		cfg, err := ReadApprovalConfig(expected.config)
		if err != nil {
			return expected, "", err
		}
		permission.Action = actionApproveStep
		permission.Approvers = slices.Clone(cfg.Approvers)
	}
	who, err := s.require(ctx, permission)
	return expected, who, err
}

// MayApprove answers whether the caller could approve this step now, by the
// same checks a vote passes, without voting. The check is not consequential,
// so it records no authorization receipt. A refusal is a *DecisionDenied;
// any other error means eligibility could not be read.
func (s *Service) MayApprove(ctx context.Context, id ids.ID) error {
	_, _, err := s.prepareDecision(ctx, id, actionApproveStep)
	return err
}

// MayCreateAndRun answers whether the caller could open a Change in the
// project and start its first Run, by the checks CreateAndRun makes, without
// doing either. A caller about to spend on choosing what to create asks
// first. It records no authorization receipt.
func (s *Service) MayCreateAndRun(ctx context.Context, projectID ids.ID) error {
	if _, err := s.requireActor(ctx, operationPermission{Action: actionCreateChange, ProjectID: projectID}); err != nil {
		return err
	}
	_, err := s.requireActor(ctx, operationPermission{Action: actionStartRun, ProjectID: projectID})
	return err
}

func (s *Service) Override(ctx context.Context, in Override) (*Step, error) {
	if strings.TrimSpace(in.Reason) == "" {
		return nil, errOverrideReason
	}
	expected, who, err := s.prepareDecision(ctx, in.StepID, actionOverrideStep)
	if err != nil {
		return nil, err
	}
	step, err := (stepDecisionStore{s.db, s.clocked}).override(ctx, expected, who, in.Reason)
	if err != nil {
		return nil, err
	}
	s.advanceCommitted(RunRef{ID: step.RunID, ProjectID: expected.scope.ProjectID})
	return step, nil
}

func (s *Service) Approve(ctx context.Context, in Approval) (*Step, error) {
	if in.Decision != VoteApprove && in.Decision != VoteReject {
		return nil, errApprovalDecision
	}
	expected, who, err := s.prepareDecision(ctx, in.StepID, actionApproveStep)
	if err != nil {
		return nil, err
	}
	var also []string
	if in.Decision == VoteApprove && in.Shown != nil {
		// Read before the vote's transaction: resolving the landing reads the
		// project's way of working, which is no part of the vote's locks. The
		// vote records the sentences the person was shown, and only while they
		// are still what the landing asks.
		asks := s.landingMeansFor(ctx, expected.scope.RunID, expected.scope.StepID)
		if !slices.Equal(in.Shown, asks) {
			return nil, errLandingQuestionChanged
		}
		also = asks
	}
	var shown *ChangeAdds
	if in.Decision == VoteApprove {
		// Read before the vote's transaction, like the landing's sentences:
		// it reads Git. The vote records the list only while it is still
		// the list the person was shown, for the commit the vote names.
		run, step, err := s.readDecisionStep(ctx, expected)
		if err != nil {
			return nil, err
		}
		if shown, err = s.adds.forStep(ctx, run, step); err != nil {
			return nil, err
		}
		switch {
		case shown == nil:
		case in.Adds == "":
			return nil, errAddsNotShown
		case in.Adds != shown.Digest:
			return nil, errAddsChanged
		}
	}
	var accepted string
	if shown != nil {
		// The list and the vote name one commit: for an acceptance, the
		// branch it read; a landing's vote names its pinned candidate,
		// which the vote's transaction checks is still the list's head.
		accepted = shown.Head
	} else if len(also) > 0 {
		// An acceptance carries to a checked landing only for the commit it
		// was given on. Uncommitted files are
		// not part of it: a landing sweeps them into a newer commit.
		if accepted, err = s.pins.acceptedTip(ctx, RunRef{ID: expected.scope.RunID, ProjectID: expected.scope.ProjectID}); err != nil {
			return nil, err
		}
	}
	result, err := (stepDecisionStore{s.db, s.clocked}).approve(ctx, expected, who, in, also, accepted, shown)
	if err != nil {
		return nil, err
	}
	s.dispatchRework(result.rework)
	s.advanceCommitted(RunRef{ID: result.step.RunID, ProjectID: expected.scope.ProjectID})
	return result.step, nil
}
