package deployment

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/deploy"
)

func TestDeploymentExecutionLostSettlementQueuesObservationWithoutReexecution(t *testing.T) {
	t.Parallel()
	for _, rejected := range []bool{false, true} {
		t.Run(fmt.Sprint(rejected), func(t *testing.T) {
			f, dep := deploymentOutcomeFixture(t)
			useRecoveryJobs(t, f)
			release, err := NewReleases(f.db).Start(context.Background(), ReleaseDraft{Source: ReleaseFromBundle, ProjectID: dep.ProjectID, DeploymentID: dep.ID, Deployment: "main", Revision: "selected-revision"}, "prepared for settlement")
			if err != nil {
				t.Fatal(err)
			}
			releaseID := release.ID
			store := f.outcomes
			in := deploymentStartFor(dep, fmt.Sprintf("release-%d", releaseID), "selected")
			in.Inputs.ReleaseID = releaseID
			notified := 0
			runner := Runner{Store: store, Applied: func() { notified++ }}
			executions := 0
			result, err := runner.Run(context.Background(), in, func(ctx context.Context, at Attempt, checkpoint deploy.Checkpoint) (deploy.Observation, error) {
				executions++
				if err := checkpoint(map[string]any{"providerID": "original"}); err != nil {
					t.Fatal(err)
				}
				dbtest.FailNextStatement(t, f.db, "INSERT INTO release_events")
				if rejected {
					return deploy.Observation{}, deploy.Terminal(errors.New("provider rejected rollout"))
				}
				return deploymentCompletionFor(at, "selected").Observed, nil
			})
			if !errors.Is(err, database.ErrInjectedFailure) || deploy.IsTerminal(err) || result.Outcome != Uncertain || notified != 0 {
				t.Fatalf("%+v %v notified=%d", result, err, notified)
			}
			if a := storedAttempt(t, f.db, result.Attempt.ID); a.Status != AttemptUncertain.String() || a.FinishedAt.Valid {
				t.Fatalf("lost settlement attempt: %s finished=%v", a.Status, a.FinishedAt.Valid)
			}
			if d := storedDeployment(t, f.db, dep.ID); d.ObservedGeneration != 0 || d.Readiness != ReadinessUncertain {
				t.Fatalf("lost settlement deployment: observed %d %s", d.ObservedGeneration, d.Readiness)
			}
			if r := storedRelease(t, f.db, dep.ProjectID, releaseID); r.State != Releasing {
				t.Fatalf("lost settlement release: %s", r.State)
			}
			wantN(t, "available recovery jobs", queuedJobs(t, f.db, RecoveryJob, "available"), 1)
			wantN(t, "release events", everyReleaseEvent(t, f.db), 1)
			replay, err := runner.Run(context.Background(), in, func(context.Context, Attempt, deploy.Checkpoint) (deploy.Observation, error) {
				executions++
				return deploy.Observation{}, nil
			})
			if err != nil || replay.Created || executions != 1 || replay.Attempt != result.Attempt {
				t.Fatalf("replay repeated execution: %+v %v calls=%d", replay, err, executions)
			}

			recoverer := Recoverer{Store: store, Source: RecoverySourceFor(store, func(context.Context, *Deployment) (deploy.Driver, error) {
				return storedRecoveryDriver{resume: func(_ context.Context, _ deploy.Placement, generation int64, noted map[string]any) (deploy.Observation, error) {
					if generation != result.Attempt.Generation || noted["providerID"] != "original" {
						t.Fatal("recovery lost identity")
					}
					if rejected {
						return deploy.Observation{}, deploy.Terminal(errors.New("provider rejected rollout"))
					}
					return deploymentCompletionFor(result.Attempt, "selected").Observed, nil
				}}, nil
			})}
			recovered, err := recoverer.Run(context.Background(), result.Attempt.ID)
			wantOutcome, wantRelease := "recovered", "released"
			if rejected {
				wantOutcome, wantRelease = "failed", "failed"
			}
			if err != nil || recovered.Outcome != wantOutcome {
				t.Fatalf("recovery: %+v %v", recovered, err)
			}
			if r := storedRelease(t, f.db, dep.ProjectID, releaseID); r.State.String() != wantRelease {
				t.Fatalf("recovered release: %s, want %s", r.State, wantRelease)
			}
			wantN(t, "release events", releaseEvents(t, f.db, releaseID), 2)
		})
	}
}
