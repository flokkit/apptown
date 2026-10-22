package workflowexec

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/workflow"
)

// settleProduced records the outcome of work already done and paid for: an
// agent's or reviewer's invocation, and the publication of its commit. The
// invocation and the publication are never repeated to record it; only the
// outcome transaction (outcome and artifacts together) is tried again, with
// the same outcome, under the attempt's fence.
//
// Terminal behaviour, in order:
//   - the outcome is recorded: nil, fellBack false;
//   - the step is no longer this attempt's: ErrStepAttemptLost, nothing more
//     is written;
//   - the outcome still cannot be recorded within the bound: the step is
//     recorded as an error saying so (fellBack true), so it does not stay
//     running after its work finished. Its produced artifacts are not kept;
//     a retry of the step runs it again.
//   - even that error cannot be recorded: the write's error is returned and
//     logged, and the step stays running for startup repair to fail, as a
//     script task's unrecorded outcome stays for recovery.
func settleProduced(ctx context.Context, s workflow.Settlement, stepID ids.ID, finish func() error, fallback func(reason string) error) (fellBack bool, err error) {
	err = s.Retry(ctx, finish)
	if err == nil || errors.Is(err, workflow.ErrStepAttemptLost) {
		return false, err
	}
	reason := fmt.Sprintf("the step's work finished, but its result could not be recorded: %v", err)
	if ferr := s.Retry(ctx, func() error { return fallback(reason) }); ferr != nil {
		if !errors.Is(ferr, workflow.ErrStepAttemptLost) {
			log.Printf("step %d: %s; recording that failed too: %v; it stays running for startup repair", stepID, reason, ferr)
		}
		return false, ferr
	}
	return true, nil
}

// settleProducedStep is settleProduced for an Executor's step: the fallback
// is an error outcome naming what could not be recorded. It returns the
// status actually recorded, for the attempt's telemetry.
func (a *Executor) settleProducedStep(task *workflow.Execution, status workflow.StepState, finish func() error) (workflow.StepState, error) {
	fellBack, err := settleProduced(task.Context(), a.Settle, task.Step().ID, finish, func(reason string) error {
		return task.Finish(workflow.EncodeOutcome(workflow.StatusError, map[string]any{"reason": reason, "unrecorded": status.String()}, nil))
	})
	if fellBack {
		return workflow.StatusError, nil
	}
	return status, err
}
