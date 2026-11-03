package workflowexec

import (
	"testing"

	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/execution"
)

func TestHandoverReviewWaitsForBootThenRunsAfterOverride(t *testing.T) {
	for _, status := range []workflow.StepState{workflow.StatusFail, workflow.StatusError, workflow.StatusNeedsHuman} {
		t.Run(status.String(), func(t *testing.T) {
			t.Parallel()
			calls := 0
			work, ctx, steps, _, _ := reviewFixtureWith(t, true, func(req execution.Request) execution.Outcome {
				calls++
				return reviewFile(t, req, `{"verdict":"pass","summary":"fine","findings":[]}`)
			}, nil, func(task *workflow.Execution) {
				if _, err := task.Claim(); err != nil {
					t.Fatal(err)
				}
				if err := task.Finish(workflow.EncodeOutcome(status, map[string]any{"reason": "boot could not run"}, nil)); err != nil {
					t.Fatal(err)
				}
			})
			boot, err := work.Step(ctx, steps["boot"])
			if err != nil {
				t.Fatal(err)
			}
			if err := work.DispatchStarted(boot.Step.RunID); err != nil {
				t.Fatal(err)
			}
			work.Wait()
			got, err := work.Step(ctx, steps["review"])
			if err != nil {
				t.Fatal(err)
			}
			if got.Step.Status != workflow.StatusPending || calls != 0 {
				t.Fatalf("unrun review must stay pending: status=%s calls=%d", got.Step.Status, calls)
			}
			if _, err := work.Override(ctx, workflow.Override{StepID: steps["boot"], Reason: "boot checked by person"}); err != nil {
				t.Fatal(err)
			}
			work.Wait()
			got, err = work.Step(ctx, steps["review"])
			if err != nil {
				t.Fatal(err)
			}
			if calls == 0 || got.Step.Status != workflow.StatusPass {
				t.Fatalf("review did not run after boot resolved: status=%s calls=%d", got.Step.Status, calls)
			}
		})
	}
}
