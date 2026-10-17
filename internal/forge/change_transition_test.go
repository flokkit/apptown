package forge

import (
	"errors"
	"testing"

	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/workflow"
)

func TestChangeCompletionCannotShipInactiveOrClosedWork(t *testing.T) {
	t.Parallel()
	for _, state := range []workflow.ChangeState{workflow.ChangeProposed, workflow.ChangeShipped, workflow.ChangeAbandoned} {
		t.Run(state.String(), func(t *testing.T) {
			t.Parallel()
			spec := schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{{Name: "finish"}}}
			a, _, run := completionScenario(t, spec)
			if run.Status != workflow.RunRunning {
				t.Fatalf("admitted landing status = %s, want running", run.Status)
			}
			baseline := make(map[string]int)
			for _, table := range []string{"run_events", "releases", "river_job"} {
				var count int
				// sql-guarantee: a refused completion must add no row to any of these tables, including rows no owner read lists.
				if err := a.DB.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
					t.Fatal(err)
				}
				baseline[table] = count
			}
			// sql-guarantee: Keep the admitted landing run live and closed_at NULL while changing only Change state; lifecycle commands close work and write receipts, masking the completion state guard.
			completionExec(t, a.DB, `UPDATE changes SET state=? WHERE id=?`, state, run.ChangeID)
			if err := completeWorkflowForTest(a.Work, run); !errors.Is(err, workflow.ErrChangeTransition) {
				t.Fatalf("inactive Change shipped: %v", err)
			}
			requireRuns(t, a, 1, func(r workflow.RunSummary) bool { return r.ID == run.ID && r.Status == workflow.RunRunning })
			if ch := changeRecord(t, a, run.ChangeID); ch.State != state || ch.ClosedAt.Valid {
				t.Fatalf("refused completion moved the change: %+v", ch)
			}
			for _, table := range []string{"run_events", "releases", "river_job"} {
				// sql-guarantee: a refused completion must add no row to any of these tables, including rows no owner read lists.
				completionCount(t, a.DB, baseline[table], `SELECT COUNT(*) FROM `+table)
			}
		})
	}
}
