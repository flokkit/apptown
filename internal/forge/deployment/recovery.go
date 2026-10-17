package deployment

import (
	"context"
	"errors"
	"fmt"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/deploy"
)

var ErrAttemptAbsent = errors.New("deployment attempt is absent")

type Snapshot struct {
	Attempt    Attempt
	Status     AttemptState
	Checkpoint map[string]any
}

// RecoveryTarget binds the recorded deployment to observation. Preparing it
// may read admitted inputs and bind a provider, but must perform no provider I/O.
// It contains no source checkout or capability to create/apply a deployment.
type RecoveryTarget struct {
	ReleaseID         ids.ID
	DesiredGeneration int64
	Observer          Observer
}

type RecoverySource interface {
	LoadAttempt(context.Context, ids.ID) (Snapshot, error)
	PrepareObservation(context.Context, Attempt) (RecoveryTarget, error)
}

type Observer interface {
	Resume(context.Context, Attempt, map[string]any, func(string, ...any)) (deploy.Observation, error)
}

// Each operation is an atomic persistence boundary. Complete/Fail include the
// release and receipts; Uncertain includes the durable recovery job. Implementors
// recheck generation/attempt ownership inside the transaction, after provider I/O.
type SettlementStore interface {
	Complete(context.Context, Completion) (Outcome, error)
	Fail(context.Context, Attempt, error) (Outcome, error)
	Uncertain(context.Context, Attempt, error) (Outcome, error)
}

type Reporter interface {
	Log(releaseID ids.ID, format string, args ...any)
	Applied()
}

type RecoveryResult struct {
	Attempt ids.ID `json:"attempt"`
	Outcome string `json:"outcome"`
	Reason  string `json:"reason,omitempty"`
}

// Recoverer can only observe existing effects. Jobs own retry and lifetime.
type Recoverer struct {
	Source   RecoverySource
	Store    SettlementStore
	Reporter Reporter
}

func (r Recoverer) Run(ctx context.Context, id ids.ID) (RecoveryResult, error) {
	result := RecoveryResult{Attempt: id}
	snapshot, err := r.Source.LoadAttempt(ctx, id)
	if errors.Is(err, ErrAttemptAbsent) {
		result.Outcome, result.Reason = "absent", "the deployment attempt no longer exists"
		return result, nil
	}
	if err != nil {
		return result, err
	}
	if !snapshot.Status.Open() {
		result.Outcome = snapshot.Status.String()
		return result, nil
	}
	at := snapshot.Attempt
	var releaseID ids.ID
	logf := func(format string, args ...any) {
		if r.Reporter != nil {
			r.Reporter.Log(releaseID, "attempt %d: "+format, append([]any{id}, args...)...)
		}
	}
	uncertain := func(cause error) (RecoveryResult, error) {
		out, err := r.Store.Uncertain(ctx, at, cause)
		if err != nil {
			return result, fmt.Errorf("recording uncertainty: %w (observation: %w)", err, cause)
		}
		result.Outcome, result.Reason = string(out), cause.Error()
		if out != Uncertain {
			return result, nil
		}
		logf("deployment outcome is uncertain: %v; automatic recovery will only check the existing deployment", cause)
		return result, cause
	}
	target, err := r.Source.PrepareObservation(ctx, at)
	releaseID = target.ReleaseID
	if err != nil {
		return uncertain(err)
	}
	if target.DesiredGeneration != at.Generation {
		out, err := r.Store.Fail(ctx, at, deploy.Terminal(errors.New("superseded before observation")))
		result.Outcome = string(out)
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	o, err := target.Observer.Resume(ctx, at, snapshot.Checkpoint, logf)
	if cancelled := ctx.Err(); cancelled != nil {
		return result, cancelled
	}
	if err != nil {
		if !deploy.IsTerminal(err) {
			return uncertain(err)
		}
		out, recordErr := r.Store.Fail(ctx, at, err)
		result.Outcome, result.Reason = string(out), err.Error()
		if recordErr != nil {
			return result, recordErr
		}
		if out == Applied {
			result.Outcome = "failed"
			logf("provider confirmed failure after restart: %v", err)
		}
		return result, nil
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	out, err := r.Store.Complete(ctx, Completion{Attempt: at, Observed: o})
	if err != nil {
		return uncertain(fmt.Errorf("recording observed deployment: %w", err))
	}
	result.Outcome = string(out)
	if out == Applied {
		result.Outcome = "recovered"
		if r.Reporter != nil {
			r.Reporter.Applied()
		}
		logf("live — %s serves generation %d (finished after a restart)", o.URL, at.Generation)
	}
	return result, nil
}
