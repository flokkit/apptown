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

type Action int

const (
	ActionWait Action = iota
	ActionPhase
	ActionStuck
	ActionBackoff
	ActionLanded
	ActionVerify
	ActionPrepare
	ActionRecheckLanding
)

// Decision is what the facts say to do with one item.
type Decision struct {
	Action Action
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
func Decide(o Observation) Decision {
	it := o.Item
	wait := Decision{Action: ActionWait}
	stuck := func(reason string) Decision {
		return Decision{Action: ActionStuck, Reason: reason}
	}
	phase := func(state, reason string) Decision {
		return Decision{Action: ActionPhase, State: state, Reason: reason}
	}
	backoff := func(reason string) Decision {
		if it.TransientRetries >= remMaxTransients {
			return stuck("the model provider kept refusing: " + strs.Truncate(reason, 200))
		}
		return Decision{Action: ActionBackoff, Reason: reason}
	}
	if it.State == ItemVerifying {
		return Decision{Action: ActionVerify}
	}
	if it.State == ItemStuck {
		if o.ChangeFound && o.ChangeState == workflow.ChangeShipped && o.AttemptOutcome == "stuck" {
			return Decision{Action: ActionLanded}
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
		return Decision{Action: ActionLanded}
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
			return Decision{Action: ActionRecheckLanding, Reason: text}
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
			return Decision{Action: ActionPrepare}
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

// Applied is what an applied decision left behind.
type Applied struct {
	Item      Item
	RunID     ids.ID
	Decision  Decision
	Rechecked bool
}

type ReconcileRecords interface {
	Observe(context.Context, Item) (*Observation, error)
	Apply(context.Context, Observation, Decision) (*Applied, error)
}

type Reconciler struct {
	records  ReconcileRecords
	prepare  func(ids.ID) error
	verify   func(context.Context, Item) error
	dispatch func(ids.ID)
	cooldown func(string)
}

func NewReconciler(records ReconcileRecords, prepare func(ids.ID) error, verify func(context.Context, Item) error, dispatch func(ids.ID), cooldown func(string)) *Reconciler {
	return &Reconciler{records: records, prepare: prepare, verify: verify, dispatch: dispatch, cooldown: cooldown}
}

func (r *Reconciler) Reconcile(ctx context.Context, item Item) error {
	observed, err := r.records.Observe(ctx, item)
	if err != nil || observed == nil {
		return err
	}
	applied, err := r.records.Apply(ctx, *observed, Decide(*observed))
	if err != nil || applied == nil {
		return err
	}
	switch applied.Decision.Action {
	case ActionVerify, ActionLanded:
		return r.verify(ctx, applied.Item)
	case ActionPrepare:
		return r.prepare(applied.RunID)
	case ActionBackoff:
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
