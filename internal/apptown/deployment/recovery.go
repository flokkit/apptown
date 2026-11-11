package deployment

import (
	"context"
	"errors"
	"fmt"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/deploy"
)

var errAttemptAbsent = errors.New("deployment attempt is absent")

type snapshot struct {
	Attempt    Attempt
	Status     AttemptState
	Checkpoint map[string]any
}

// recoveryTarget binds the recorded deployment to observation. Preparing it
// may read admitted inputs and bind a provider, but must perform no provider I/O.
// Observation contains no source checkout; the separately checked retry restores it.
type recoveryTarget struct {
	ReleaseID ids.ID
	TargetID  ids.ID
	Observer  observer
}

type recoveryReader interface {
	LoadAttempt(context.Context, ids.ID) (snapshot, error)
	PrepareObservation(context.Context, Attempt) (recoveryTarget, error)
}

type observer interface {
	Resume(context.Context, Attempt, map[string]any, func(string, ...any)) (deploy.Observation, error)
}

// Each operation is an atomic persistence boundary. Complete/Fail include the
// release and receipts; Uncertain includes the durable recovery job. Implementors
// recheck generation/attempt ownership inside the transaction, after provider I/O.
type settlementStore interface {
	Complete(context.Context, Completion) (deploymentOutcome, error)
	Fail(context.Context, Attempt, error) (deploymentOutcome, error)
	Uncertain(context.Context, Attempt, error) (deploymentOutcome, error)
}

type reporter interface {
	Log(releaseID ids.ID, format string, args ...any)
	Applied(ids.ID)
}

type RecoveryResult struct {
	Attempt ids.ID `json:"attempt"`
	Outcome string `json:"outcome"`
	Reason  string `json:"reason,omitempty"`
}

// recoverer observes first, then permits one durable restart retry when the
// selected version has not come up. Jobs own delivery and lifetime.
type recoverer struct {
	retry    func(context.Context, Attempt) (deploy.Observation, error)
	source   recoveryReader
	store    settlementStore
	reporter reporter
}

func (r recoverer) Run(ctx context.Context, id ids.ID) (RecoveryResult, error) {
	result := RecoveryResult{Attempt: id}
	snapshot, err := r.source.LoadAttempt(ctx, id)
	if errors.Is(err, errAttemptAbsent) {
		result.Outcome, result.Reason = "absent", "the deployment attempt no longer exists"
		return result, nil
	}
	if err != nil {
		return result, err
	}
	if !snapshot.Status.open() {
		result.Outcome = snapshot.Status.String()
		return result, nil
	}
	at := snapshot.Attempt
	var releaseID ids.ID
	logf := func(format string, args ...any) {
		if r.reporter != nil {
			r.reporter.Log(releaseID, "attempt %d: "+format, append([]any{id}, args...)...)
		}
	}
	uncertain := func(cause error) (RecoveryResult, error) {
		out, err := r.store.Uncertain(ctx, at, cause)
		if err != nil {
			return result, fmt.Errorf("recording uncertainty: %w (observation: %w)", err, cause)
		}
		result.Outcome, result.Reason = string(out), cause.Error()
		if out != outcomeUncertain {
			return result, nil
		}
		logf("deployment outcome is uncertain: %v; checking the recorded deployment", cause)
		return result, cause
	}
	target, err := r.source.PrepareObservation(ctx, at)
	releaseID = target.ReleaseID
	if err != nil {
		return uncertain(err)
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	_, restarted := snapshot.Checkpoint["_restart"]
	if restarted {
		logf("interrupted by a restart — checking the provider for generation %d", at.Generation)
	}
	o, err := target.Observer.Resume(ctx, at, snapshot.Checkpoint, logf)
	if errors.Is(err, deploy.ErrNotApplied) && restarted && r.retry != nil && ctx.Err() == nil {
		o, err = r.retry(ctx, at)
	}
	if cancelled := ctx.Err(); cancelled != nil {
		return result, cancelled
	}
	if err != nil {
		if !deploy.IsTerminal(err) {
			return uncertain(err)
		}
		out, recordErr := r.store.Fail(ctx, at, err)
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
	out, err := r.store.Complete(ctx, Completion{Attempt: at, Observed: o})
	if err != nil {
		return uncertain(fmt.Errorf("recording observed deployment: %w", err))
	}
	result.Outcome = string(out)
	if out == Applied {
		result.Outcome = "recovered"
		if r.reporter != nil {
			r.reporter.Applied(target.TargetID)
		}
		logf("live — %s serves generation %d (finished after a restart)", o.URL, at.Generation)
	}
	return result, nil
}
