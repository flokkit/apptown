package workflow

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
)

// Merging into the default branch and deploying are acts a person must
// answer for. The engine performs either only when the run carries one of
// two facts, checked at the effect itself (the merge admission and a deploy
// step's claim), whichever door started the run and whenever it resumes:
//
//   - a person's approval recorded in this run or a run that called it: a
//     passing approval step whose recorded approver (or overrider) is a
//     person, including a vote carried from accepted work, whose eligibility
//     was checked when it was carried; or
//   - the run (or the root that called it) was admitted by someone holding
//     change:land on its Change, recorded on the run when it was created
//     (runs.landing_authority).
//
// Agents, chats, step runs and AppTown's own triggers hold no landing
// authority and cannot approve, so their runs merge or deploy only after a
// person approves. A run with neither fact stops at the effect with a plain
// reason; nothing merges or deploys.
//
// Starting a run also reads its definition and refuses early, with the same
// reason, when the starter lacks landing authority and the definition would
// reach an effect before any required approval. That is a courtesy; the
// effect check is the rule.

// errNoAssent is a merge or deploy refused for want of either fact.
var errNoAssent = errors.New("no person has approved this run, and it was not started by someone who may land this change; a person must approve it, or start the landing themselves")

// assentEvidenceIn answers whether run, in projectID, may merge or deploy
// now: nil when it carries either fact, errNoAssent when it does not. An
// eval's authority is a fact only where projectID is a copy of that eval
// (eval_authority.go); anywhere else it is none.
func assentEvidenceIn(ctx context.Context, q database.Queryer, runID, projectID ids.ID) error {
	queries := workflowsql.New(q.SQL())
	for id, depth := runID, 0; !id.IsZero() && depth <= maxCallDepth+1; depth++ {
		facts, err := queries.RunLandingAuthority(ctx, id)
		if err != nil {
			return database.Missing(err)
		}
		if _, isEval := evalOfAuthority(facts.LandingAuthority); isEval {
			holds, err := evalAuthorityHolds(ctx, q, facts.LandingAuthority, projectID)
			if err != nil {
				return err
			}
			if holds {
				return nil
			}
		} else if facts.LandingAuthority != "" {
			return nil
		}
		steps, err := readSteps(ctx, q, id)
		if err != nil {
			return err
		}
		for _, step := range steps {
			if approvedByAPerson(step) {
				return nil
			}
		}
		id = facts.ParentRunID
	}
	return errNoAssent
}

// approvedByAPerson: a passing approval decided by a person, by vote or by
// a person's override.
func approvedByAPerson(step *Step) bool {
	if step.Type != "approval" || step.Status != StatusPass {
		return false
	}
	if step.OverrideBy != "" {
		return isPerson(step.OverrideBy)
	}
	evidence, err := ReadApprovalEvidence(step.Evidence)
	if err != nil {
		return false
	}
	for _, vote := range evidence.Decisions {
		if vote.Decision == VoteApprove && votedInPerson(vote) {
			return true
		}
	}
	return false
}

// votedInPerson is whether a vote is a person's own assent: a vote by a
// principal, judged by the way it was given (access.Actor.Assents), so a yes
// through a chat or an agent is not one. A vote naming no principal or no
// way is no one's assent.
func votedInPerson(vote ApprovalVote) bool {
	return !vote.Principal.IsZero() && vote.Via != "" && access.Actor{Principal: vote.Principal, Via: vote.Via}.Assents()
}

// isPerson: attribution naming a person, not AppTown itself, a step run
// or a chat.
func isPerson(who string) bool {
	return who != "" && who != "apptown-system" && !strings.HasPrefix(who, "run/") && !strings.Contains(who, " via console/")
}

// markLandingAuthorityIn records who admitted run holding change:land.
func markLandingAuthorityIn(ctx context.Context, tx *database.Tx, runID ids.ID, by string) error {
	if by == "" {
		return nil
	}
	holder, err := identity.ActorIn(ctx, tx, by)
	if err != nil {
		return err
	}
	n, err := workflowsql.New(tx.SQL()).MarkRunLandingAuthority(ctx, workflowsql.MarkRunLandingAuthorityParams{ID: runID, By: by, ByPrincipal: holder.Principal, ByVia: holder.Via})
	if err == nil && n != 1 {
		err = fmt.Errorf("run %d: landing authority not recorded", runID)
	}
	return err
}

// landingBy is the verified caller's receipt name when they hold
// change:land on the change, "" otherwise. Asking writes no receipt.
func (s *Service) landingBy(ctx context.Context, req StartRequest) string {
	if !req.parentRunID.IsZero() || req.ChangeID.IsZero() {
		return ""
	}
	actor, err := s.requireActor(ctx, operationPermission{Action: actionLandUnassented, ProjectID: req.ProjectID, ChangeID: req.ChangeID})
	if err != nil {
		return ""
	}
	return actor.Receipt
}

// maxCallDepth bounds how far calls are followed; deeper is an effect.
const maxCallDepth = 4

// calleeSpec resolves a called workflow as its child start would.
type calleeSpec func(name string) (*schema.Spec, error)

// unassentedEffect names the first effect a run of spec would reach before
// any required approval step, or "" when every effect waits for one. A child
// run never merges; the effects of what it calls are its own. An approval
// that a stage's explicit gate does not require does not count: the engine
// leaves the stage without it.
func unassentedEffect(spec *schema.Spec, child bool, callee calleeSpec, depth int) (string, error) {
	approved := false
	for _, st := range spec.Stages {
		// Effects that run in a stage happen before its approvals are known
		// to have passed: stages are the unit of order.
		for _, step := range append(append([]schema.Step{}, st.OnEnter...), st.Steps...) {
			if approved {
				break
			}
			switch step.Uses {
			case "deploy":
				return fmt.Sprintf("deploy (step %q)", step.Name), nil
			case "workflow/call":
				name, _ := step.With["workflow"].(string)
				if depth >= maxCallDepth || callee == nil || name == "" {
					return fmt.Sprintf("call another workflow (step %q)", step.Name), nil
				}
				called, err := callee(name)
				if err != nil {
					return fmt.Sprintf("call %q, which could not be read (step %q)", name, step.Name), nil
				}
				effect, err := unassentedEffect(called, true, callee, depth+1)
				if err != nil {
					return "", err
				}
				if effect != "" {
					return fmt.Sprintf("call %q, which would %s", name, effect), nil
				}
			}
		}
		for _, step := range st.Steps {
			if step.Uses == "approval" && gateRequires(&st, step.Name) {
				approved = true
			}
		}
		next, err := nextStage(spec, st.Name, child)
		if err != nil {
			return "", err
		}
		if next.Merge && !approved {
			return fmt.Sprintf("merge into the default branch after stage %q", st.Name), nil
		}
	}
	return "", nil
}

// gateRequires mirrors evaluateGate: an explicit gate requires only the
// steps it names; without one, every step is required.
func gateRequires(st *schema.Stage, name string) bool {
	if st.Gate == nil || len(st.Gate.Require) == 0 {
		return true
	}
	for _, required := range st.Gate.Require {
		if required == name {
			return true
		}
	}
	return false
}

// requireLanding: the early refusal for a root start whose definition would
// reach an effect before any required approval.
func (s *Service) requireLanding(ctx context.Context, req StartRequest, effect string) error {
	_, err := s.requireActor(ctx, operationPermission{Action: actionLandUnassented, ProjectID: req.ProjectID, ChangeID: req.ChangeID})
	var denied *access.Denied
	if errors.As(err, &denied) {
		return &access.Denied{Code: denied.Code, Reason: fmt.Sprintf("workflow %q would %s with no person's approval first, which needs landing authority (%s); request a landing instead", req.WorkflowName, effect, denied.Reason)}
	}
	return err
}
