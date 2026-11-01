package remediation

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/strs"
)

type remediationAction int

const (
	actionWait remediationAction = iota
	actionPhase
	actionStuck
	actionBackoff
	actionLanded
	actionVerify
	actionPrepare
	ActionRecheckLanding
)

// decision is what the facts say to do with one item.
type decision struct {
	Action remediationAction
	State  string
	Reason string
}

// Observation is the facts one decision is taken from.
type Observation struct {
	Item           Item
	ChangeFound    bool
	ChangeState    workflow.ChangeState
	AttemptOutcome string
	Execution      workflow.ExecutionObservation
}

// Decisions consume facts only: no App, database, clock or execution hooks.
func Decide(o Observation) decision {
	it := o.Item
	wait := decision{Action: actionWait}
	stuck := func(reason string) decision {
		return decision{Action: actionStuck, Reason: reason}
	}
	phase := func(state, reason string) decision {
		return decision{Action: actionPhase, State: state, Reason: reason}
	}
	backoff := func(reason string) decision {
		if it.TransientRetries >= remMaxTransients {
			return stuck("the model provider kept refusing: " + strs.Truncate(reason, 200))
		}
		return decision{Action: actionBackoff, Reason: reason}
	}
	if it.State == itemVerifying {
		return decision{Action: actionVerify}
	}
	if it.State == ItemStuck {
		if o.ChangeFound && o.ChangeState == workflow.ChangeShipped && o.AttemptOutcome == "stuck" {
			return decision{Action: actionLanded}
		}
		return wait
	}
	if it.State != ItemWorking && it.State != ItemAccepting && it.State != ItemLanding {
		return wait
	}
	if !o.ChangeFound {
		return stuck("its Change disappeared")
	}
	switch o.ChangeState {
	case workflow.ChangeAbandoned:
		return stuck(fmt.Sprintf("change %d was abandoned", it.ChangeID))
	case workflow.ChangeShipped:
		return decision{Action: actionLanded}
	case workflow.ChangeLanding:
		if it.State != ItemLanding {
			return phase(ItemLanding, "")
		}
	}
	execution := o.Execution
	var failed, live, approvals, others, agentFailed int
	var failures []string
	for _, step := range execution.Steps {
		if step.Status == workflow.StatusRunning || step.Status == workflow.StatusNeedsHuman {
			live++
		}
		if step.Type == "approval" && (step.Status == workflow.StatusPending || step.Status == workflow.StatusNeedsHuman) {
			approvals++
		}
		if step.Type != "approval" && (step.Status == workflow.StatusPending || step.Status == workflow.StatusRunning || step.Status == workflow.StatusNeedsHuman) {
			others++
		}
		if step.Status == workflow.StatusFail || step.Status == workflow.StatusError {
			failed++
			if strings.HasPrefix(step.Type, "agent/") {
				agentFailed++
			}
			var evidence struct {
				Reason        string `json:"reason"`
				ProviderError string `json:"providerError"`
			}
			_ = json.Unmarshal(step.Evidence, &evidence)
			reason := strs.FirstNonBlank(evidence.Reason, evidence.ProviderError)
			failures = append(failures, fmt.Sprintf("%s %s: %s", step.Name, step.Status, strs.Truncate(reason, 240)))
		}
	}
	text := strings.Join(failures, " · ")
	if o.ChangeState == workflow.ChangeLanding {
		if execution.Run.ID.IsZero() || execution.Run.Status != workflow.RunRunning || failed == 0 || live > 0 {
			return wait
		}
		if transientReason(text) && it.TransientRetries < remMaxTransients {
			return decision{Action: ActionRecheckLanding, Reason: text}
		}
		return stuck(fmt.Sprintf("the landing (run %d) is blocked: %s", execution.Run.ID, text))
	}
	if it.RunID.IsZero() {
		return stuck("no run was started for its Change")
	}
	if execution.Run.ID.IsZero() {
		return stuck("its run disappeared")
	}
	switch execution.Run.Status {
	case workflow.RunRunning:
		if execution.Unready {
			return decision{Action: actionPrepare}
		}
		if failed > 0 && others == 0 {
			if transientReason(text) {
				return backoff(text)
			}
			if execution.Reworks >= 3 {
				return stuck("a gate kept failing after the lane's rework: " + text)
			}
			if agentFailed == 0 {
				return stuck("the lane is blocked on a gate the agent cannot rework: " + text)
			}
		} else if approvals > 0 && others == 0 && it.State != ItemAccepting {
			return phase(ItemAccepting, "waiting on you: accept")
		} else if others > 0 && it.State == ItemAccepting {
			return phase(ItemWorking, "")
		}
	case workflow.RunDone:
		if it.State != ItemAccepting {
			return phase(ItemAccepting, "accepted; landing is starting")
		}
	case workflow.RunAbandoned:
		if transientReason(text) {
			return backoff(text)
		}
		if strings.Contains(strings.ToLower(text), "decision") || strings.Contains(strings.ToLower(text), "question") {
			return stuck("the agent needs a decision: " + text)
		}
		return stuck(strs.FirstNonBlank(text, "its run "+execution.Run.Status.String()))
	}
	return wait
}

// applied is what an applied decision left behind.
type applied struct {
	Item      Item
	RunID     ids.ID
	Decision  decision
	Rechecked bool
}

type reconcileRecords interface {
	Observe(context.Context, Item) (*Observation, error)
	Apply(context.Context, Observation, decision) (*applied, error)
}

type reconciler struct {
	records  reconcileRecords
	prepare  func(ids.ID) error
	verify   func(context.Context, Item) error
	dispatch func(ids.ID)
	cooldown func(string)
}

func newReconciler(records reconcileRecords, prepare func(ids.ID) error, verify func(context.Context, Item) error, dispatch func(ids.ID), cooldown func(string)) *reconciler {
	return &reconciler{records: records, prepare: prepare, verify: verify, dispatch: dispatch, cooldown: cooldown}
}

func (r *reconciler) reconcile(ctx context.Context, item Item) error {
	observed, err := r.records.Observe(ctx, item)
	if err != nil || observed == nil {
		return err
	}
	applied, err := r.records.Apply(ctx, *observed, Decide(*observed))
	if err != nil || applied == nil {
		return err
	}
	switch applied.Decision.Action {
	case actionVerify, actionLanded:
		return r.verify(ctx, applied.Item)
	case actionPrepare:
		return r.prepare(applied.RunID)
	case actionBackoff:
		r.cooldown(applied.Decision.Reason)
	case ActionRecheckLanding:
		if applied.Rechecked {
			r.cooldown(applied.Decision.Reason)
			r.dispatch(applied.RunID)
		}
	}
	return nil
}

// transientReason says whether a failure text looks like a provider refusing
// (rate limit, overload, credit) — something waiting fixes, not the project.
func transientReason(text string) bool {
	t := strings.ToLower(text)
	for _, m := range []string{"429", "rate limit", "rate_limit", "too many requests", "overloaded", "credit balance", "quota", "capacity", "503", "502"} {
		if strings.Contains(t, m) {
			return true
		}
	}
	return false
}
