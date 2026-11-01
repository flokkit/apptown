package deployment

import (
	"context"
	"errors"
	"fmt"
)

// recoveryReport counts what startup recovery did: attempts queued for
// observation, and deployments whose records contradicted each other and
// now need a person (each logged and in the ledger).
type recoveryReport struct{ Queued, NeedsAttention int }

type startupStore interface {
	InterruptedAttempts(ctx context.Context) ([]Attempt, error)
	ContradictorySettlements(ctx context.Context) ([]contradiction, error)
	Uncertain(context.Context, Attempt, error) (deploymentOutcome, error)
	NeedsAttention(context.Context, Attempt, error) error
}

// PrepareRecovery queues observation before new work is admitted. It has no
// provider collaborator, so it cannot hold boot on a slow rollout or repeat a
// deployment effect. A deployment whose records contradict each other does
// not stop startup: it is marked as needing attention (its readiness
// uncertain, a ledger line), and the others are recovered as usual. Only a
// failure to read the records, or to record the mark, stops startup.
func PrepareRecovery(ctx context.Context, store startupStore) (recoveryReport, error) {
	var report recoveryReport
	attempts, err := store.InterruptedAttempts(ctx)
	if err != nil {
		return report, err
	}
	stranded, err := store.ContradictorySettlements(ctx)
	if err != nil {
		return report, fmt.Errorf("checking deployment settlement consistency: %w", err)
	}
	for _, c := range stranded {
		cause := fmt.Errorf("release %d is still releasing after deployment attempt %d ended %s", c.ReleaseID, c.Attempt.ID, c.Status)
		if err := store.NeedsAttention(ctx, c.Attempt, cause); err != nil {
			return report, fmt.Errorf("marking deployment %d as needing attention (%v): %w", c.Attempt.DeploymentID, cause, err)
		}
		report.NeedsAttention++
	}
	for _, at := range attempts {
		out, err := store.Uncertain(ctx, at, errors.New("execution interrupted; observing the recorded provider identity before admitting another deployment"))
		if contradicted(err) {
			if markErr := store.NeedsAttention(ctx, at, err); markErr != nil {
				return report, fmt.Errorf("marking deployment %d as needing attention (%v): %w", at.DeploymentID, err, markErr)
			}
			report.NeedsAttention++
			continue
		}
		if err != nil {
			return report, fmt.Errorf("scheduling recovery for deployment %d attempt %d: %w", at.DeploymentID, at.ID, err)
		}
		if out == outcomeUncertain {
			report.Queued++
		}
	}
	return report, nil
}

// contradicted says an error is the records disagreeing with the rules
// (an attempt open while its deployment reads settled), not a failure to
// read or write them.
func contradicted(err error) bool {
	return errors.Is(err, errReadinessTransition) || errors.Is(err, errOutcomeConflict) || errors.Is(err, errAttemptTransition)
}
