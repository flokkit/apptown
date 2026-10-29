package workflow

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/ids"
)

func TestStaleCheckRefreshDispatchAndRestart(t *testing.T) {
	t.Parallel()
	for _, failed := range []bool{false, true} {
		t.Run(fmt.Sprintf("failed=%v", failed), func(t *testing.T) {
			ctx := context.Background()
			spec := schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "finish", Steps: []schema.Step{{Name: "scan", Uses: "scan"}, {Name: "review", Uses: "agent/review"}, {Name: "work", Uses: "agent/pi"}, {Name: "accept", Uses: "approval"}}}}}
			owner, run := admitWorkflowSpec(t, spec)
			// Finish real attempts, then backdate only the checks whose age makes
			// them stale. The recent agent result remains current.
			for _, kind := range []string{"scan", "agent/review", "agent/pi"} {
				name := map[string]string{"scan": "scan", "agent/review": "review", "agent/pi": "work"}[kind]
				id := admittedStepID(t, owner, run, name)
				attempts := newStepAttempts(owner.db, func(candidate string) bool { return candidate == kind })
				step, err := readStep(ctx, owner.db, id)
				if err != nil {
					t.Fatal(err)
				}
				claim, err := attempts.Start(ctx, step, kind)
				if err != nil {
					t.Fatal(err)
				}
				if err := attempts.Finish(ctx, claim, StepOutcome{Status: StatusPass}); err != nil {
					t.Fatal(err)
				}
			}
			// sql-guarantee: Backdate only finished scan/review rows so recheck's clearing of their prior finished_at is observable; generated finishing operations use database now() without a clock parameter.
			completionExec(t, owner.db, `UPDATE steps SET finished_at=now()-interval '1 minute' WHERE run_id=$1 AND type IN ('scan','agent/review')`, run.ID)
			var calls atomic.Int32
			bind := func() *Service {
				deps := Dependencies{Automatic: []string{"scan", "agent/review", "agent/pi"}, Runners: map[string]Runner{}, Report: func(err error) { t.Error(err) }}
				for _, kind := range []string{"scan", "agent/review"} {
					deps.Runners[kind] = func(task *Execution) {
						if _, err := task.Claim(); err != nil {
							t.Error(err)
							return
						}
						calls.Add(1)
						if err := task.Finish(StepOutcome{Status: StatusPass}, nil); err != nil {
							t.Error(err)
						}
					}
				}
				service := newServiceWithAuthority(owner.db, deps, allowProject(run.ProjectID))
				service.dispatcher.prepare = nil
				service.dispatcher.advance = func(ids.ID) {}
				t.Cleanup(service.Stop)
				return service
			}
			if failed {
				service := bind()
				completionExec(t, owner.db, `ALTER TABLE run_events ADD CONSTRAINT reject_last_recheck CHECK(kind!='step.reverified' OR payload_json->>'step'!='review')`)
				if err := service.RefreshAssertions(ctx, run.ID); err == nil {
					t.Fatal("refresh failure hidden")
				}
				service.Wait()
				if calls.Load() != 0 {
					t.Fatal("uncommitted reset dispatched")
				}
				completionExec(t, owner.db, `ALTER TABLE run_events DROP CONSTRAINT reject_last_recheck`)
				if err := service.RefreshAssertions(ctx, run.ID); err != nil {
					t.Fatal(err)
				}
				service.Wait()
			} else {
				if changed, err := (reworkStore{owner.db, clocked{}}).refresh(ctx, run.ID); err != nil || !changed {
					t.Fatalf("reset: %v %v", changed, err)
				}
				restarted := bind()
				for range 2 {
					restarted.ResumePending()
					restarted.Wait()
				}
			}
			if calls.Load() != 2 {
				t.Fatalf("check executions: %d", calls.Load())
			}
			wantRunEventCount(t, owner.db, run.ID, 2, ofKind("step.reverified"))
		})
	}
}
