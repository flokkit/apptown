package workflow

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
	"flokkit.io/forge/internal/ids"
)

// A yes given in advance. A person opening an adoption Change may say now
// that, if the adoption needs no code and its work passes outright, this is
// their acceptance, and their approval of its landing. Each approval says
// for itself what such a yes means (ApprovalConfig.InAdvance); one that
// says nothing takes none. The person is shown those sentences, project by
// project, and the Change keeps what they were shown (AdvanceYes). AppTown
// casts the yes as their vote only where it witnesses the condition at the
// act, and the vote records the sentence it meant (ApprovalVote.InAdvance).
//
// The condition, the same for the working run and its landing:
//
//   - the change is empty against its base (ChangeIsEmpty), read under the
//     repository's lock at the working run's act, and the vote names that
//     commit; the landing needs no witness of its own, since carry counts a
//     yes only where the landing's pinned candidate is the commit it named;
//   - every step other than an approval passed, with no override and no
//     skip, and every check it lists passed. A failure already on main, a
//     check AppTown could not run, an error, an excused finding, a skipped
//     check or an unclear answer is not a pass, whoever's fault it was; a
//     review that left a finding is not either (passedOutright).
//
// Anything else leaves the approval for a person, with a receipt saying why.

// InAdvanceQuestion is what a yes given in advance on an adoption of a
// project would mean: the lane its Change would run, that lane's
// acceptance sentence and the landing's, in order. NotOffered says why no
// such yes can be given (a lane or landing approval that declares none).
type InAdvanceQuestion struct {
	Workflow   string   `json:"workflow"`
	Acceptance string   `json:"acceptance"`
	Landing    []string `json:"landing"`
	NotOffered string   `json:"notOffered,omitempty"`
}

// Shown is the sentences in the order a person is shown them and sends
// back: the acceptance, then the landing's.
func (q InAdvanceQuestion) Shown() []string {
	if q.NotOffered != "" {
		return nil
	}
	return append([]string{q.Acceptance}, q.Landing...)
}

// InAdvanceQuestions answers the question for a project as its adoption
// would run now.
type InAdvanceQuestions interface {
	InAdvance(ctx context.Context, projectID ids.ID) (InAdvanceQuestion, error)
}

// InAdvanceQuestion answers what a yes in advance on an adoption of the
// project would mean now.
func (s *Service) InAdvanceQuestion(ctx context.Context, projectID ids.ID) (InAdvanceQuestion, error) {
	if _, err := s.require(ctx, operationPermission{Action: actionReadProject, ProjectID: projectID}); err != nil {
		return InAdvanceQuestion{}, err
	}
	if s.inAdvance == nil {
		return InAdvanceQuestion{Landing: []string{}, NotOffered: "AppTown cannot read what a yes in advance means here"}, nil
	}
	return s.inAdvance.InAdvance(ctx, projectID)
}

// ErrInAdvanceNotAssent refuses a yes in advance from a way of acting that
// does not give the person's own assent (an agent's credential, a chat).
var ErrInAdvanceNotAssent = fault.New(fault.Forbidden, "a yes in advance is a person's own: give it signed in or from your own command line, not through an agent")

// AdvanceYes is a person's yes given in advance on an adoption Change, as
// recorded when they gave it.
type AdvanceYes struct {
	Approver  string   `json:"approver"`
	Principal ids.ID   `json:"principal"`
	Via       string   `json:"via,omitempty"`
	At        string   `json:"at"`
	Workflow  string   `json:"workflow"`
	Shown     []string `json:"shown"`
}

func (y AdvanceYes) actor() access.Actor {
	return access.Actor{Name: y.Approver, Principal: y.Principal, Via: y.Via}
}

// advanceYesFrom is the yes the verified caller gives now, for the sentences
// shown. Admission refuses one that is not the caller's own assent.
func advanceYesFrom(ctx context.Context, q database.Queryer, who string, workflow string, shown []string, at string) (*AdvanceYes, error) {
	if len(shown) == 0 {
		return nil, nil
	}
	// The verified caller as a vote records them (decision_store.go).
	actor, err := identity.ActorIn(ctx, q, who)
	if err != nil {
		return nil, err
	}
	if actor.Principal.IsZero() || !actor.Assents() {
		return nil, ErrInAdvanceNotAssent
	}
	return &AdvanceYes{Approver: who, Principal: actor.Principal, Via: actor.Via, At: at, Workflow: workflow, Shown: slices.Clone(shown)}, nil
}

// passedOutright says why steps did not pass outright, or "" when every
// step other than an approval did.
func passedOutright(steps []*Step) string {
	for _, step := range steps {
		if step.Type == "approval" {
			continue
		}
		if step.OverrideBy != "" {
			return fmt.Sprintf("%s was overridden by %s", step.Name, step.OverrideBy)
		}
		if step.Status != StatusPass {
			return fmt.Sprintf("%s is %s, not passed", step.Name, step.Status)
		}
		if step.Type == "agent/review" {
			var ev struct {
				Findings []string `json:"findings"`
			}
			if err := approvalObject(step.Evidence, &ev); err != nil {
				return fmt.Sprintf("%s's evidence could not be read", step.Name)
			}
			if len(ev.Findings) > 0 {
				return fmt.Sprintf("%s left %d finding(s)", step.Name, len(ev.Findings))
			}
		}
		if !carriesCheckEvidence(step.Type) {
			continue
		}
		evidence, err := ReadCheckEvidence(step.Evidence)
		if err != nil {
			return fmt.Sprintf("%s's check evidence could not be read", step.Name)
		}
		if evidence.CheckErrors > 0 {
			return fmt.Sprintf("%s had %d check error(s)", step.Name, evidence.CheckErrors)
		}
		for _, c := range evidence.Checks {
			// Only a pass: a failure already on main passes the gate, but
			// the yes was given on condition every check passes.
			if c.Verdict != VerdictPass {
				return fmt.Sprintf("%s: %s was %s", step.Name, c.Check, c.Verdict)
			}
		}
	}
	return ""
}

// advanceYesOn reads the yes in advance on an adoption Change, if any.
func advanceYesOn(ch *Change) *AdvanceYes {
	if !ch.LandsWithoutCode() {
		return nil
	}
	acc, err := ch.acceptance()
	if err != nil {
		return nil
	}
	return acc.InAdvance
}

type inAdvanceWithheld struct {
	StepID   ids.ID `json:"stepId"`
	Step     string `json:"step"`
	Approver string `json:"approver"`
	Reason   string `json:"reason"`
}

type inAdvanceApplied struct {
	StepID   ids.ID `json:"stepId"`
	Step     string `json:"step"`
	Approver string `json:"approver"`
	Means    string `json:"means"`
}

// answerFromAcceptance is the engine's hook for approvals a recorded yes
// answers: an eval's authority in its copy, an accepted change's acceptance
// carried to its landing, or a yes given in advance on an adoption's
// working run.
func (s *Service) answerFromAcceptance(ctx context.Context, run *Run) (bool, error) {
	if cast, err := s.landings.carryOrSettleForEval(ctx, run); cast || err != nil {
		return cast, err
	}
	return s.acceptInAdvance(ctx, run)
}

// acceptInAdvance casts a Change's yes in advance on its working run's
// waiting acceptance where the condition holds, or records once why not.
// It reports whether a vote was cast, so the engine advances again.
func (s *Service) acceptInAdvance(ctx context.Context, expected *Run) (bool, error) {
	if expected == nil || expected.Status != RunRunning || expected.ChangeID.IsZero() || !expected.ParentRunID.IsZero() {
		return false, nil
	}
	ch, err := ReadChangeInProject(ctx, s.db, ChangeRef{ID: expected.ChangeID, ProjectID: expected.ProjectID})
	if err != nil {
		return false, err
	}
	yes := advanceYesOn(ch)
	if yes == nil {
		return false, nil
	}
	// An eval's copy is settled by its eval, never by a person's yes.
	authority, err := rootAuthorityIn(ctx, s.db, expected.ID)
	if err != nil {
		return false, err
	}
	if inEval, err := evalAuthorityHolds(ctx, s.db, authority, expected.ProjectID); err != nil || inEval {
		return false, err
	}
	steps, err := readSteps(ctx, s.db, expected.ID)
	if err != nil {
		return false, err
	}
	var step *Step
	for _, st := range steps {
		if st.Stage == expected.CurrentStage && st.Status == StatusPending && isAcceptance(expected, st) {
			step = st
		}
	}
	if step == nil || !settled(steps, expected.CurrentStage) {
		return false, nil
	}
	considered, err := workflowsql.New(s.db.SQL()).InAdvanceConsidered(ctx, expected.ID)
	if err != nil || slices.Contains(considered, step.ID) {
		return false, err
	}
	withhold := func(reason string) (bool, error) {
		_, err := database.Transact(ctx, s.db, func(tx *database.Tx) (struct{}, error) {
			return struct{}{}, recordReceiptIn(ctx, tx, expected.ID, "acceptance.inadvance.withheld", inAdvanceWithheld{step.ID, step.Name, yes.Approver, reason}, s.now())
		})
		return false, err
	}
	cfg, err := ReadApprovalConfig(step.Config)
	if err != nil {
		return false, err
	}
	switch {
	case cfg.InAdvance == "":
		return withhold(fmt.Sprintf("this workflow's %s approval says nothing of a yes given in advance", step.Name))
	case !slices.Contains(yes.Shown, cfg.InAdvance):
		return withhold(fmt.Sprintf("%s was not shown what a yes in advance means here (“%s”)", yes.Approver, cfg.InAdvance))
	}
	if why := passedOutright(steps); why != "" {
		return withhold(why)
	}
	// Eligibility reads policy on other connections: asked before the lock
	// and the vote's transaction, as for a manual approval.
	if err := s.landings.eligible(ctx, expected, step, yes.actor()); err != nil {
		var denied *DecisionDenied
		if errors.As(err, &denied) {
			return withhold(denied.Error())
		}
		return false, err
	}
	// What the landing asks in advance now: the yes counts for it only if
	// those are the sentences the person was shown.
	var also []string
	if s.inAdvance != nil {
		q, err := s.inAdvance.InAdvance(ctx, expected.ProjectID)
		if err != nil {
			log.Printf("run %d: reading what the landing asks in advance: %v", expected.ID, err)
		} else if q.NotOffered == "" && len(q.Landing) > 0 && containsAll(yes.Shown, q.Landing) {
			also = q.Landing
		}
	}
	if s.abandonments.locks == nil {
		return false, nil
	}
	release, err := s.abandonments.locks.Acquire(ctx, expected.ProjectID)
	if err != nil {
		return false, err
	}
	defer release()
	source, err := ReadSourceIn(ctx, s.db, RunRef{ID: expected.ID, ProjectID: expected.ProjectID})
	if err != nil {
		return false, err
	}
	empty, err := ChangeIsEmpty(ctx, s.dataDir, source)
	if err != nil {
		return false, err
	}
	if !empty {
		return withhold("the change has code of its own: a person reads it")
	}
	tip, err := s.pins.acceptedTip(ctx, RunRef{ID: expected.ID, ProjectID: expected.ProjectID})
	if err != nil {
		return false, err
	}
	adds, err := s.adds.forStep(ctx, expected, step)
	if err != nil {
		return false, err
	}
	if adds != nil {
		tip = adds.Head
	}
	if tip == "" {
		return withhold("AppTown could not read which commit the change is at, so the yes would name none")
	}
	means := cfg.InAdvance
	result, err := database.Transact(ctx, s.db, func(tx *database.Tx) (reworkedResult, error) {
		run, err := lockCurrentRunIn(ctx, tx, expected)
		if err != nil {
			return reworkedResult{}, err
		}
		all, err := readSteps(ctx, tx, run.ID)
		if err != nil {
			return reworkedResult{}, err
		}
		// Judged again under the locks every step decision takes: an override
		// or a step that moved since the reading above is seen here.
		var current *Step
		for _, st := range all {
			if st.ID == step.ID {
				current = st
			}
		}
		if current == nil || current.Status != StatusPending || run.CurrentStage != step.Stage || !settled(all, run.CurrentStage) || passedOutright(all) != "" {
			return reworkedResult{}, ErrRunMoved
		}
		vote := ApprovalVote{Approver: yes.Approver, Principal: yes.Principal, Via: yes.Via, Decision: VoteApprove,
			Comment:      fmt.Sprintf("given in advance at %s, when the adoption was opened: it needed no code and every step passed outright", yes.At),
			AlsoApproves: also, Candidate: tip, Adds: adds, InAdvance: means}
		res, err := (reworkStore{s.db, s.clocked}).approveIn(ctx, tx, run, current, vote)
		if err != nil {
			return res, err
		}
		return res, recordReceiptIn(ctx, tx, run.ID, "acceptance.inadvance.applied", inAdvanceApplied{step.ID, step.Name, yes.Approver, means}, s.now())
	})
	if errors.Is(err, ErrRunMoved) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	s.dispatchRework(result)
	return true, nil
}

func containsAll(have, want []string) bool {
	for _, w := range want {
		if !slices.Contains(have, w) {
			return false
		}
	}
	return true
}
