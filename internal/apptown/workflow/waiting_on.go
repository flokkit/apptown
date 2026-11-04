package workflow

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/ids"
)

// WaitingOn describes attention, after workflow classification. It is not a
// lifecycle or permission to execute an act; commands check current authority.
type WaitingOn struct {
	Kind   string    `json:"kind"`
	Run    ids.ID    `json:"run"`
	Step   ids.ID    `json:"step"`
	Checks int       `json:"checks"`
	Who    []Decider `json:"who"`
	You    string    `json:"you"`
	Says   string    `json:"says"`
	Tone   string    `json:"tone"`
}

// Decider retains whether an effective approval expression names a person,
// group or role. Name is the expression's name without its kind prefix.
type Decider struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

func unknownWaitingOn() WaitingOn {
	return WaitingOn{Kind: "unknown", You: "unknown", Says: "unknown", Tone: "unknown", Who: []Decider{}}
}

func checkHold(s workflowActivityStep) bool {
	return (s.state == StatusFail || s.state == StatusError) && s.kind != "approval" && s.kind != "policy/current" && s.kind != "scm/merged"
}

// waitingOn applies lifecycle and classification precedence before wording it.
func waitingOn(ch *Change, d *RunSnapshot) WaitingOn {
	w := WaitingOn{Kind: "chat", You: "no", Who: []Decider{}, Tone: "neutral"}
	if ch != nil && ch.State.Closed() {
		w.Kind = "closed"
		return sayWaitingOn(w, Activity{}, ch)
	}
	if d != nil && d.Run.Status != RunRunning && !d.LandingQueued && ch == nil {
		w.Kind, w.Says = "closed", "finished"
		if d.Run.Status != RunDone {
			w.Says = d.Run.Status.String()
		}
		return w
	}
	if d == nil || d.Run.Status != RunRunning && !d.LandingQueued {
		return sayWaitingOn(w, Activity{}, ch)
	}
	a := d.activity
	if a.holdingStep != nil {
		w.Step = a.holdingStep.id
	}
	w.Run = a.holdingRun
	if w.Run.IsZero() {
		w.Run = d.Run.ID
	}
	switch {
	case d.LandingQueued:
		w.Kind = "queued"
	case a.Held || a.State == ActivityBlocked:
		w.Kind, w.You = "person", "unknown"
		if a.holdingStep != nil {
			w.Step = a.holdingStep.id
			if checkHold(*a.holdingStep) {
				w.Kind, w.Checks = "check", a.failedChecks
			}
		}
	case !a.Approval.IsZero():
		w.Kind, w.Step, w.You = "approval", a.Approval, "unknown"
	case a.State == ActivityReady:
		w.Kind, w.You = "ready", "unknown"
	default:
		w.Kind = "work"
	}
	return sayWaitingOn(w, a, ch)
}

// sayWaitingOn is the one source of common state wording for every client.
func sayWaitingOn(w WaitingOn, a Activity, ch *Change) WaitingOn {
	w.Tone = "neutral"
	switch w.Kind {
	case "closed":
		w.Says, w.Tone = "landed", "ok"
		if ch.ClosedAs == "record" {
			w.Says = "finished"
		}
		if ch.State == ChangeAbandoned {
			w.Says, w.Tone = ch.State.Label(ch.ClosedAs), "neutral"
		}
	case "check":
		w.Tone = "bad"
		if w.Checks > 1 {
			w.Says = fmt.Sprintf("%d checks failed", w.Checks)
		} else if a.holdingStep.state == StatusError {
			w.Says = a.holdingStep.name + " could not run"
		} else {
			w.Says = a.holdingStep.name + " failed"
		}
	case "person":
		w.Says, w.Tone = a.Detail, "warn"
	case "chat":
		w.Says = "chat open"
	case "work":
		w.Says = "working"
	case "queued":
		w.Says = "queued to land"
	case "ready":
		w.Says, w.Tone = "ready to continue", "warn"
	case "approval":
		w.Tone = "warn"
		names := []string{}
		collective := false
		for _, who := range w.Who {
			names = append(names, who.Name)
			collective = collective || who.Kind != "person"
		}
		switch {
		case w.You == "yes" && collective:
			w.Says = "waiting for you or " + strings.Join(names, " or ")
		case w.You == "yes":
			w.Says = "ready for your approval"
		case len(names) > 0:
			w.Says = "waiting for " + strings.Join(names, " or ")
		default:
			w.Says = "waiting for approval"
		}
	}
	return w
}

func eligibility(err error) string {
	if err == nil {
		return "yes"
	}
	var decision *DecisionDenied
	var denied *access.Denied
	if errors.As(err, &decision) || errors.As(err, &denied) || errors.Is(err, ErrCallerRequired) {
		return "no"
	}
	return "unknown"
}

// readerWaitingOn reads eligibility for the classified holding run, including
// a child's. It never creates acts on the parent or treats global acts as grants.
func (s *Service) readerWaitingOn(ctx context.Context, ch *Change, d *RunSnapshot) WaitingOn {
	w := waitingOn(ch, d)
	if w.Kind != "approval" && w.Kind != "check" && w.Kind != "person" && w.Kind != "ready" {
		return w
	}
	w.You = "unknown"
	run, err := readScopedRun(ctx, s.db, w.Run, d.Run.ProjectID)
	if err != nil {
		return w
	}
	if w.Kind == "approval" {
		step, err := readStep(ctx, s.db, w.Step)
		if err != nil {
			return w
		}
		cfg, err := ReadApprovalConfig(step.Config)
		if err != nil {
			return w
		}
		policy, err := workflowsql.New(s.db.SQL()).ApproverEligibility(ctx, workflowsql.ApproverEligibilityParams{ProjectID: run.ProjectID, ChangeID: run.ChangeID})
		if err != nil {
			return w
		}
		for _, expression := range approvalExpressions(cfg.Approvers, policy.DefaultApprovers) {
			who := Decider{Kind: "person", Name: expression}
			if name, ok := strings.CutPrefix(expression, "group:"); ok {
				who.Kind, who.Name = "group", name
			}
			if name, ok := strings.CutPrefix(expression, "role:"); ok {
				who.Kind, who.Name = "role", name
			}
			w.Who = append(w.Who, who)
		}
		w.You = eligibility(s.MayApprove(ctx, w.Step))
		return sayWaitingOn(w, d.activity, ch)
	}
	steps, err := readSteps(ctx, s.db, run.ID)
	if err != nil {
		return w
	}
	spec, err := run.Definition()
	if err != nil {
		return w
	}
	acts := nextActs(run, steps, d.activity, func(kind string) bool {
		return assertionKind(kind) || s.stepAttempts.automatic != nil && s.stepAttempts.automatic(kind)
	}, spec.Role == "landing")
	// Send-back is change-scoped and offered only until the landing merged.
	if !run.ChangeID.IsZero() {
		change, err := ReadChangeIn(ctx, s.db, run.ChangeID)
		if err != nil {
			return w
		}
		merged, err := ChangeMergedIn(ctx, s.db, run.ProjectID, run.ChangeID)
		if err != nil {
			return w
		}
		acts = append(acts, ChangeActs(change, &RunSnapshot{Run: run, State: d.activity.State, held: d.activity.Held}, merged, false)...)
	}
	w.You = "no"
	for _, act := range acts {
		action := actionRunStep
		switch act.Act {
		case nextOverride:
			if act.StepID != w.Step {
				continue
			}
			action = actionOverrideStep
		case nextSendBack:
			action = actionSendBackChange
		case NextAdvance:
			action = actionAdvanceRun
		case nextRestartLanding:
			action = actionLandUnassented
		case nextRetry:
			if act.StepID != w.Step {
				continue
			}
		case nextRework:
		default:
			continue
		}
		_, err := s.require(ctx, operationPermission{Action: action, ProjectID: run.ProjectID, ChangeID: run.ChangeID, RunID: run.ID, StepID: act.StepID})
		answer := eligibility(err)
		if answer == "yes" {
			w.You = "yes"
			break
		}
		if answer == "unknown" {
			w.You = "unknown"
		}
	}
	return w
}

// ChangeWaitingOn applies the change's lifecycle to an already checked snapshot.
func (s *Service) ChangeWaitingOn(ctx context.Context, ch *Change, d *RunSnapshot) WaitingOn {
	if d != nil && !ch.State.Closed() && (d.Run.Status == RunRunning || d.LandingQueued) {
		return d.WaitingOn
	}
	return waitingOn(ch, d)
}

// mayVote includes current-round person identity, just as ApprovalEvidence.cast.
func (s *Service) mayVote(ctx context.Context, id ids.ID) error {
	expected, who, err := s.prepareDecision(ctx, id, actionApproveStep)
	if err != nil {
		return err
	}
	run, step, err := s.readDecisionStep(ctx, expected)
	if err != nil {
		return err
	}
	if run.Status != RunRunning || step.Stage != run.CurrentStage || step.Status != StatusPending {
		return &DecisionDenied{Reason: "this approval is not waiting for a vote"}
	}
	voter, err := identity.ActorIn(ctx, s.db, who)
	if err != nil {
		return err
	}
	evidence, err := ReadApprovalEvidence(step.Evidence)
	if err != nil {
		return err
	}
	if evidence.HasVoted(voter.Principal) {
		return &DecisionDenied{Reason: "you have already voted in this round"}
	}
	return nil
}
