package workflow

import (
	"context"
	"errors"
	"time"

	"flokkit.io/forge/internal/ids"
)

// DefaultAttemptWatch is how often Work looks at a running step's run while
// its runner runs.
const DefaultAttemptWatch = 3 * time.Second

// errRunnerReturned is the cause of a claimed execution's context once its
// runner returned. The fenced writes still answer through it (fenceContext).
var errRunnerReturned = errors.New("the step's runner returned")

// watchAttempt gives a step's runner a context Work cancels, with cause
// ErrStepAttemptLost, once the step is left pending or running in a run that
// is no longer running: its run was abandoned or its change closed.
// Abandonment ends a run's call steps itself; every other running step ends
// when its runner returns (endAbandonedAttempt), and without this a runner
// that waits on its context (a local agent, a slot) would run to its end or
// its timeout in an abandoned run. A step this execution finished is not
// pending or running, so a run that completes is never cancelled under its
// runner. A read failure keeps waiting: it is not permission to stop
// someone's work. unwatch stops the watch when the runner returns, and
// releases the context when the runner claimed its step, with cause
// errRunnerReturned: work that waits on it stops, and an outcome recorded
// later by whoever kept the execution still reaches the fence. An execution
// returned unclaimed keeps its context, so whoever holds it may still run it.
func (s *Service) watchAttempt(parent context.Context, stepID ids.ID) (_ context.Context, unwatch func(release bool)) {
	ctx, cancel := context.WithCancelCause(parent)
	done := make(chan struct{})
	go func() {
		tick := time.NewTicker(s.attemptWatch)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-tick.C:
				if s.leftInEndedRun(context.WithoutCancel(ctx), stepID) {
					cancel(ErrStepAttemptLost)
					return
				}
			}
		}
	}()
	return ctx, func(release bool) {
		close(done)
		if release {
			cancel(errRunnerReturned)
		}
	}
}

// leftInEndedRun reports whether the step is still pending or running while
// its run is no longer running.
func (s *Service) leftInEndedRun(ctx context.Context, stepID ids.ID) bool {
	step, err := ReadStepIn(ctx, s.db, stepID)
	if err != nil || (step.Status != StatusPending && step.Status != StatusRunning) {
		return false
	}
	run, err := ReadRunIn(ctx, s.db, step.RunID)
	return err == nil && run.Status != RunRunning
}
