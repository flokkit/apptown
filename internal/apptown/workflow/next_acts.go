package workflow

import "github.com/flokkit/apptown/internal/ids"

// NextActKind names something a person can do now.
type NextActKind string

const (
	// NextApprove decides the approval the run waits on
	// (POST /steps/{id}/approve).
	NextApprove NextActKind = "approve"
	// nextOverride overrules a failed check that blocks the stage, with a
	// reason (POST /steps/{id}/override).
	nextOverride NextActKind = "override"
	// nextRework gives the stage's agent another round
	// (POST /steps/{id}/run).
	nextRework NextActKind = "rework"
	// nextRetry runs a step that stopped the stage again
	// (POST /steps/{id}/run).
	nextRetry NextActKind = "retry"
	// NextAdvance moves the run on from a stage that has all it needs
	// (POST /runs/{id}/advance).
	NextAdvance NextActKind = "advance"
	// nextRestartLanding lands the change again under the current rules,
	// after they changed while the landing waited
	// (POST /runs/{id}/restart-landing).
	nextRestartLanding NextActKind = "restart-landing"
	// NextLand starts the change's landing (POST /changes/{id}/land).
	NextLand NextActKind = "land"
	// nextSendBack sends the change's work back to its agent with a note
	// (POST /changes/{id}/sendback).
	nextSendBack NextActKind = "send-back"
	// nextAbandon closes a blocked change with a reason
	// (POST /changes/{id}/abandon). Unlike the other acts it is offered only
	// to a reader who may abandon the change.
	nextAbandon NextActKind = "abandon"
)

// NextAct is one act available now. A step act names its step; a run act
// (recheck, advance, restart-landing) names none and acts on the run the
// snapshot is for; a change act (land, send-back) acts on the change.
// Clients submit it as it is rather than work out a target; the command is
// still checked when it arrives, so an act read a moment ago may be refused.
type NextAct struct {
	Act    NextActKind `json:"act"`
	StepID ids.ID      `json:"stepId"`
	Step   string      `json:"step"`
}

// nextActs is what a person can do on a running run, from the activity the
// classifier gave it and its current stage's steps. A run working on its own
// offers nothing: its next move is AppTown's. runnable says which step
// kinds a step run accepts (automatic kinds and assertions); landing says the
// run is a landing.
func nextActs(run *Run, steps []*Step, a Activity, runnable func(string) bool, landing bool) []NextAct {
	acts := []NextAct{}
	if run.Status != RunRunning {
		return acts
	}
	if a.State == ActivityReady {
		return append(acts, NextAct{Act: NextAdvance})
	}
	if a.State != ActivityWaiting && a.State != ActivityBlocked {
		return acts
	}
	var stage []*Step
	ownApproval := false
	running := false
	for _, s := range steps {
		if s.Stage == run.CurrentStage {
			stage = append(stage, s)
			ownApproval = ownApproval || s.ID == a.Approval
			running = running || s.Status == StatusRunning
		}
	}
	var agent *Step
	rulesChanged := false
	var retries []NextAct
	for _, s := range stage {
		if s.Type == "agent/pi" {
			agent = s // the stage agent is its last, as rework picks it
		}
		stopped := s.Status == StatusFail || s.Status == StatusError || s.Status == StatusNeedsHuman
		switch {
		// Waiting on this run's own approval (not a called workflow's), each
		// approval still asking may be decided, in the stage's order.
		case a.State == ActivityWaiting && ownApproval && s.Type == "approval" && s.Status == StatusPending:
			acts = append(acts, NextAct{Act: NextApprove, StepID: s.ID, Step: s.Name})
		case a.State == ActivityBlocked && s.Type != "approval" && s.OverrideBy == "" && overridable(s.Status):
			acts = append(acts, NextAct{Act: nextOverride, StepID: s.ID, Step: s.Name})
		}
		if a.State == ActivityBlocked && stopped && s.Type != "approval" && !running && runnable != nil && runnable(s.Type) {
			retries = append(retries, NextAct{Act: nextRetry, StepID: s.ID, Step: s.Name})
		}
		rulesChanged = rulesChanged || (landing && s.Type == "policy/current" && stopped)
	}
	acts = append(acts, retries...)
	// Another round is refused while any step of the stage runs (retryIn); a
	// call step runs while the run waits on its child. A queued agent never
	// gets here: the classifier calls its run queued.
	if agent != nil && !running {
		acts = append(acts, NextAct{Act: nextRework, StepID: agent.ID, Step: agent.Name})
	}
	if a.State == ActivityBlocked && rulesChanged {
		acts = append(acts, NextAct{Act: nextRestartLanding})
	}
	return acts
}

// overridable: a check that failed or errored, which an override may move
// to pass by Step.Override's own transition.
func overridable(state StepState) bool {
	if state != StatusFail && state != StatusError {
		return false
	}
	_, err := state.transition(moveStepOverride, StatusPass)
	return err == nil
}

// ChangeActs is what a person can do on the change itself, from its state,
// its current run's snapshot (nil when it has none) and whether its landing
// merged: land an in-progress change nothing is running on and no landing is
// queued for; send back the work of an open change whose current run is
// blocked, until its landing has merged (the work is then on main); and, to
// a reader who mayAbandon, close that blocked change, so work that can never
// pass is not left stuck.
func ChangeActs(ch *Change, current *RunSnapshot, merged, mayAbandon bool) []NextAct {
	acts := []NextAct{}
	running := current != nil && current.Run != nil && current.Run.Status == RunRunning
	queued := current != nil && current.LandingQueued
	if ch.State == ChangeInProgress && !running && !queued {
		acts = append(acts, NextAct{Act: NextLand})
	}
	if ch.State.canRun() && !merged && running && current.State == ActivityBlocked {
		acts = append(acts, NextAct{Act: nextSendBack})
		if mayAbandon {
			acts = append(acts, NextAct{Act: nextAbandon})
		}
	}
	return acts
}
