package workflow

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/schema"
)

// Activity is derived from current Work facts, never a second stored lifecycle.
// The one classifier supplies the same vocabulary to lists, detail and consoles.
type ActivityState string

const (
	ActivityWorking      ActivityState = "working"
	activityAgentWorking ActivityState = "agent working"
	activityQueued       ActivityState = "queued"
	ActivityBlocked      ActivityState = "blocked"
	ActivityWaiting      ActivityState = "waiting on you"
	ActivityReady        ActivityState = "ready to advance"
	ActivityAbandoned    ActivityState = "abandoned"
	ActivityDone         ActivityState = "done"
)

type Activity struct {
	State  ActivityState
	Detail string
	// Approval is the approval step the activity waits on when it reads
	// "waiting on you": the run's own, or a called workflow's when the
	// waiting is carried up from it. Zero otherwise.
	Approval ids.ID
	// Held distinguishes a stopped stage from a pending approval.
	Held         bool `json:"-"`
	holdingRun   ids.ID
	holdingStep  *workflowActivityStep
	failedChecks int
	goingLive    bool // the current stage is the landing definition's final stage
}

func (a Activity) Active() bool {
	return a.State != ActivityAbandoned && a.State != ActivityDone
}

type slotWait func(ids.ID) (binding string, ahead int, waiting bool)
type workflowActivityStep struct {
	id                   ids.ID
	attempt              int64
	name, kind, override string
	state                StepState
	evidence             json.RawMessage // call identity, placement and hold reason/actions
	finished             sql.NullTime
}
type workflowActivityRun struct {
	readError         error // malformed facts affect this run and its callers only
	id                ids.ID
	projectID         ids.ID
	parentID          ids.ID
	parentStepID      ids.ID
	parentAttempt     int64
	state             RunState
	abandonmentReason string
	steps             []workflowActivityStep
	stage             *schema.Stage // current stage's pinned declaration, running runs only
	checks            []Check       // its stage.entered receipt; nil before receipts named checks
	// awaitingAcceptance: a landing started from accepted work that has not
	// yet taken that acceptance; its approvals are about to be judged.
	awaitingAcceptance bool
}
type workflowActivityGraph map[ids.ID]*workflowActivityRun

func (g workflowActivityGraph) classify(id ids.ID, wait slotWait, path map[ids.ID]bool) (out Activity, err error) {
	run := g[id]
	if run == nil {
		return Activity{}, fmt.Errorf("run %d is missing from the activity snapshot", id)
	}
	if run.readError != nil {
		return Activity{}, run.readError
	}
	if path[id] {
		return Activity{}, fmt.Errorf("run %d has a cyclic workflow call", id)
	}
	defer func() {
		if err == nil && out.holdingRun.IsZero() {
			out.holdingRun = id
		}
	}()
	path[id] = true
	defer delete(path, id)
	if run.state == RunAbandoned {
		return Activity{State: ActivityAbandoned, Detail: run.abandonmentReason}, nil
	}
	if run.state == RunDone {
		return Activity{State: ActivityDone}, nil
	}
	var pendingAgent, pendingWork, pendingApproval, failing, rechecking string
	var pendingApprovalID ids.ID
	var queuedStep *workflowActivityStep
	reworked := latestAgentWork(run.steps)
	prerequisitesSettled := true
	var held *workflowActivityStep
	for _, step := range run.steps {
		ready := prerequisitesSettled
		prerequisitesSettled = prerequisitesSettled && dispatchSettled(step.state)
		if ready && step.state == StatusPending && activityShowsPendingWork(step.kind) && pendingWork == "" {
			pendingWork = step.name
		}
		switch {
		case step.state == StatusRunning:
			if step.kind == "workflow/call" {
				var call struct {
					ChildRunID ids.ID `json:"childRunId"`
					Workflow   string `json:"workflow"`
				}
				if len(step.evidence) == 0 || step.evidence[0] != '{' {
					return Activity{}, fmt.Errorf("call step %d evidence must be an object", step.id)
				}
				if err := json.Unmarshal(step.evidence, &call); err != nil {
					return Activity{}, fmt.Errorf("call step %d evidence: %w", step.id, err)
				}
				if !call.ChildRunID.IsZero() {
					child := g[call.ChildRunID]
					if child == nil || child.projectID != run.projectID || child.parentID != run.id || child.parentStepID != step.id || child.parentAttempt != step.attempt {
						return Activity{}, fmt.Errorf("call step %d does not own current child %d", step.id, call.ChildRunID)
					}
					if child.state == RunRunning {
						activity, err := g.classify(child.id, wait, path)
						if err != nil {
							return Activity{}, err
						}
						if activity.Detail != "" {
							activity.Detail += " "
						}
						activity.Detail += "(in " + call.Workflow + ")"
						return activity, nil
					}
				}
			}
			state := ActivityWorking
			if step.kind == "agent/pi" {
				state = activityAgentWorking
			}
			return Activity{State: state, Detail: step.name, holdingStep: &step}, nil
		case step.kind == "approval" && step.state == StatusPending:
			pendingApproval, pendingApprovalID = step.name, step.id
		case (step.kind == "agent/pi" || step.kind == "script" || step.kind == "agent/review") && step.state == StatusPending:
			if !ready {
				continue
			}
			if step.kind != "agent/review" && queuedStep == nil {
				pendingAgent = step.name
			}
			if wait != nil {
				if binding, ahead, waiting := wait(step.id); waiting && queuedStep == nil {
					queued := step
					queuedStep = &queued
					pendingAgent = step.name + " — " + slotWaitWords(binding, ahead)
				}
			}
		case step.stale(reworked):
			rechecking = step.name
		case step.state == StatusFail || step.state == StatusError || step.state == StatusNeedsHuman:
			failing = step.name + " (" + step.state.String() + ")"
			if held == nil {
				hold := step
				held = &hold
			}
		}
	}
	if pendingAgent != "" {
		return Activity{State: activityQueued, Detail: pendingAgent, holdingStep: queuedStep}, nil
	}
	// Only work whose prerequisites permit dispatch counts as the next work.
	if pendingWork != "" {
		detail := pendingWork + " next"
		if failing != "" {
			detail += "; " + failing + " so far"
		}
		return Activity{State: ActivityWorking, Detail: detail}, nil
	}
	if held != nil {
		count := 0
		for _, step := range run.steps {
			if step.override == "" && !step.stale(reworked) && checkHold(step) {
				count++
			}
		}
		return Activity{State: ActivityWaiting, Detail: held.holdDetail(), Held: true, holdingStep: held, failedChecks: count}, nil
	}
	// A verdict on the work before its rework is about to be checked again;
	// the stage is not blocked by it (wait called such runs wedged).
	if rechecking != "" {
		return Activity{State: ActivityWorking, Detail: "checking " + rechecking + " again after the rework"}, nil
	}
	if run.awaitingAcceptance {
		return Activity{State: ActivityWorking, Detail: "starting the landing with the change's acceptance"}, nil
	}
	// Each run's own gate judges it before its activity reaches a parent, so a
	// child's refusal is what the parent shows, in every view.
	if pendingApproval != "" {
		return g.judgedByGate(id, Activity{State: ActivityWaiting, Detail: pendingApproval, Approval: pendingApprovalID}), nil
	}
	return g.judgedByGate(id, Activity{State: ActivityReady}), nil
}

// holdDetail names the step and its recorded reason. Clients render recovery
// choices from the offered acts, rather than a second list in this sentence.
func (step workflowActivityStep) holdDetail() string {
	detail := step.name + " (" + step.state.String() + ")"
	var evidence struct {
		Reason string `json:"reason"`
	}
	if json.Unmarshal(step.evidence, &evidence) == nil && evidence.Reason != "" {
		detail += ": " + evidence.Reason
	}
	return detail
}

// latestAgentWork is when the stage's agent last finished work that passed.
func latestAgentWork(steps []workflowActivityStep) sql.NullTime {
	var latest sql.NullTime
	for _, step := range steps {
		if step.kind == "agent/pi" && step.state == StatusPass && step.finished.Valid && (!latest.Valid || step.finished.Time.After(latest.Time)) {
			latest = step.finished
		}
	}
	return latest
}

// stale reports a failed verdict given before the stage's latest agent work:
// the one AppTown resets and checks again after a rework (recheck.go's
// StaleChecksForUpdate), so it no longer judges the work.
func (step workflowActivityStep) stale(reworked sql.NullTime) bool {
	if step.state != StatusFail || step.override != "" || !reworked.Valid || !step.finished.Valid || !step.finished.Time.Before(reworked.Time) {
		return false
	}
	switch step.kind {
	case "approval", "agent/review", "scan", "tests", "preview", "guidance/conformance":
		return true
	}
	return false
}

// slotWaitWords is the sentence for a step waiting on a binding.
func slotWaitWords(binding string, ahead int) string {
	s := "waiting for a slot on " + binding
	switch ahead {
	case 0:
		return s + ", next in line"
	case 1:
		return s + ", one ahead"
	default:
		return fmt.Sprintf("%s, %d ahead", s, ahead)
	}
}

// activityShowsPendingWork distinguishes automatic work from decisions and assertions in the activity display.
func activityShowsPendingWork(kind string) bool { return kind != "approval" && !assertionKind(kind) }
