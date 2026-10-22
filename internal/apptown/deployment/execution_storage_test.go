package deployment

import (
	"context"
	"errors"
	"testing"

	"github.com/flokkit/apptown/internal/deploy"
)

func TestDeploymentExecutionCancellationQueuesRecoveryInsteadOfPublishing(t *testing.T) {
	t.Parallel()
	f, dep := deploymentOutcomeFixture(t)
	useRecoveryJobs(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	notified := 0
	result, err := (Runner{Store: f.outcomes, Applied: func() { notified++ }}).Run(ctx, deploymentStartFor(dep, "cancelled", "selected"),
		func(_ context.Context, at Attempt, checkpoint deploy.Checkpoint) (deploy.Observation, error) {
			if err := checkpoint(map[string]any{"providerID": "original"}); err != nil {
				t.Fatal(err)
			}
			cancel()
			return deploymentCompletionFor(at, "selected").Observed, nil
		})
	if !errors.Is(err, context.Canceled) || result.Outcome != Uncertain || notified != 0 {
		t.Fatalf("%+v %v notified=%d", result, err, notified)
	}
	if a := storedAttempt(t, f.db, result.Attempt.ID); a.Status != AttemptUncertain.String() {
		t.Fatalf("cancelled attempt: %s", a.Status)
	}
	if d := storedDeployment(t, f.db, dep.ID); d.ObservedGeneration != 0 || d.Readiness != ReadinessUncertain {
		t.Fatalf("cancelled deployment: observed %d %s", d.ObservedGeneration, d.Readiness)
	}
	wantN(t, "available recovery jobs", queuedJobs(t, f.db, RecoveryJob, "available"), 1)
	wantN(t, "releases", everyRelease(t, f.db), 0)
}
