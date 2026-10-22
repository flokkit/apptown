package httpapi

import (
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestDurationSeconds(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name             string
		created, updated string
		want             int
	}{
		{"one minute", "2026-01-01T00:00:00Z", "2026-01-01T00:01:00Z", 60},
		{"same instant", "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z", 0},
		{"bad created", "not-a-time", "2026-01-01T00:01:00Z", 0},
		{"bad updated", "2026-01-01T00:00:00Z", "nope", 0},
	}
	for _, c := range cases {
		if got := durationSeconds(c.created, c.updated); got != c.want {
			t.Errorf("%s: durationSeconds(%q,%q)=%d, want %d", c.name, c.created, c.updated, got, c.want)
		}
	}
}
func TestWorkflowRunRowsFormatsTypedSummariesAndTimesRunningOnesByWorksClock(t *testing.T) {
	t.Parallel()
	rows := workflowRunRows([]workflow.RunSummary{{
		ID: idstest.N(7), Number: 40, ChangeID: idstest.N(9), ChangeNumber: 12, WorkflowName: "build", ItemTitle: "Ship it",
		Status: workflow.RunDone, CurrentStage: "finish", State: workflow.ActivityDone,
		StateDetail: "completed", ProjectSlug: "game",
		CreatedAt: "2026-01-01T00:00:00Z", UpdatedAt: "2026-01-01T00:00:45Z",
	}, {
		ID: idstest.N(8), Status: workflow.RunRunning, CreatedAt: "2026-01-01T00:00:00Z", UpdatedAt: "2026-01-01T00:00:10Z",
	}}, time.Date(2026, 1, 1, 0, 2, 0, 0, time.UTC))
	if len(rows) != 2 {
		t.Fatalf("rows: %+v", rows)
	}
	got := rows[0]
	if got.RunID != idstest.N(7) || got.RunNumber != 40 || got.ChangeID != idstest.N(9) || got.ChangeNumber != 12 || got.Workflow != "build" || got.Title != "Ship it" || got.Status != workflow.RunDone || got.Stage != "finish" || got.State != workflow.ActivityDone || got.Detail != "completed" || got.Project != "game" || got.StartedAt != "2026-01-01T00:00:00Z" || got.DurationS != 45 {
		t.Fatalf("formatted row: %+v", got)
	}
	// A running run has run until now, Work's clock, not the wall clock.
	if running := rows[1]; running.DurationS != 120 {
		t.Fatalf("a running run's duration by the clock given: %+v", running)
	}
}
