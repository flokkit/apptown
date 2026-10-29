package httpapi

import (
	"database/sql"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/usage"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"

	"github.com/flokkit/apptown/internal/stamp"
)

func TestPercentile(t *testing.T) {
	t.Parallel()
	if percentile(nil, 0.5) != 0 {
		t.Fatal("empty is 0")
	}
	s := []int64{10, 20, 30, 40, 50, 60, 70, 80, 90, 100}
	if percentile(s, 0.5) != 50 || percentile(s, 0.9) != 90 || percentile(s, 1) != 100 {
		t.Fatalf("median %d p90 %d max %d", percentile(s, 0.5), percentile(s, 0.9), percentile(s, 1))
	}
}

// Presentation consumes typed ledger observations: a step's
// duration, the wait before it, an approval's wait for a person, a rework
// counted as an extra attempt, and the run's total.
func TestWorkflowTimingSummary(t *testing.T) {
	t.Parallel()
	born := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	runs := []workflow.RunTiming{{ID: idstest.N(1), Workflow: "quick-change", CreatedAt: born, Steps: []workflow.StepTiming{
		{ID: idstest.N(10), Stage: "apply", Name: "apply-change", Type: "agent/pi", Status: workflow.StatusPass, StartedAt: stamp.Of(born.Add(70 * time.Second)), FinishedAt: stamp.Of(born.Add(100 * time.Second))},
		{Stage: "apply", Name: "review-change", Type: "agent/review", Status: workflow.StatusPass, StartedAt: stamp.Of(born.Add(102 * time.Second)), FinishedAt: stamp.Of(born.Add(162 * time.Second))},
		{Stage: "apply", Name: "accept", Type: "approval", Status: workflow.StatusPass, FinishedAt: stamp.Of(born.Add(462 * time.Second)), Reworks: 1},
	}}}
	wt := summarizeWorkflowTiming("quick-change", 1, runs, map[ids.ID]usage.StepCost{idstest.N(10): {CostMicros: 15000, Incomplete: 1}})
	if wt.Runs != 1 || len(wt.Steps) != 3 {
		t.Fatalf("runs %d steps %d: %+v", wt.Runs, len(wt.Steps), wt.Steps)
	}
	by := map[string]stepTiming{}
	for _, s := range wt.Steps {
		by[s.Name] = s
	}
	if by["accept"].Reworks != 1 || by["apply-change"].CostMicros != 15000 || by["apply-change"].CostIncomplete != 1 || by["review-change"].CostMicros != 0 || by["apply-change"].MedianMs != 30000 {
		t.Fatalf("apply-change: %+v", by["apply-change"])
	}
	if by["accept"].WaitMedianMs != 300000 {
		t.Fatalf("the approval waited 5 minutes for a person: %+v", by["accept"])
	}
	if wt.TotalMedianMs != 462000 {
		t.Fatalf("total: %d", wt.TotalMedianMs)
	}
}

func TestWorkflowTimingGroupsAndRepeatedSteps(t *testing.T) {
	t.Parallel()
	born := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	runs := []workflow.RunTiming{{ID: idstest.N(1), Workflow: "busy", CreatedAt: born}}
	costs := map[ids.ID]usage.StepCost{}
	for i := range 20 {
		step := workflow.StepTiming{ID: idstest.N(uint64(100 + i)), Stage: "apply", Name: "build", Type: "agent/pi", Status: workflow.StatusPass, StartedAt: stamp.Of(born.Add(2 * time.Second)), FinishedAt: stamp.Of(born.Add(3 * time.Second)), Reworks: 2}
		costs[step.ID] = usage.StepCost{CostMicros: 15000}
		runs = append(runs, workflow.RunTiming{ID: idstest.N(uint64(i + 2)), Workflow: "busy", CreatedAt: born, Steps: []workflow.StepTiming{step, step}})
	}
	runs = append(runs, workflow.RunTiming{ID: idstest.N(22), Workflow: "zeta"}, workflow.RunTiming{ID: idstest.N(23), Workflow: "alpha"})
	summaries := summarizeTimings(runs, costs, "", 1)
	if len(summaries) != 3 || summaries[0].Workflow != "busy" || summaries[1].Workflow != "alpha" || summaries[2].Workflow != "zeta" {
		t.Fatalf("workflow ordering: %+v", summaries)
	}
	busy := summaries[0]
	if busy.Runs != 21 || len(busy.Steps) != 1 || busy.Steps[0].Runs != 20 || busy.Steps[0].Reworks != 40 || busy.Steps[0].CostMicros != 600000 || busy.Steps[0].MedianMs != 1000 {
		t.Fatalf("empty run, duplicate steps, reworks or costs: %+v", busy)
	}
	if got := summarizeTimings(nil, nil, "missing", 7); len(got) != 1 || got[0].Runs != 0 || got[0].Steps != nil || got[0].Days != 7 {
		t.Fatalf("missing workflow: %+v", got)
	}
}

func TestWorkflowTimingTimeline(t *testing.T) {
	t.Parallel()
	born := time.Date(2020, 1, 2, 3, 4, 5, 123456000, time.UTC)
	at := func(t time.Time) stamp.Time { return stamp.Time{NullTime: sql.NullTime{Time: t, Valid: true}} }
	runs := []workflow.RunTiming{{ID: idstest.N(1), Workflow: "timing", CreatedAt: born, Steps: []workflow.StepTiming{
		{Stage: "apply", Name: "build", Type: "agent/pi", Status: workflow.StatusPass, StartedAt: at(born.Add(250 * time.Millisecond)), FinishedAt: at(born.Add(1500 * time.Millisecond)), Reworks: 1},
		{Stage: "review", Name: "build", Type: "agent/review", Status: workflow.StatusPass, StartedAt: at(born.Add(time.Second)), FinishedAt: at(born.Add(2 * time.Second)), Reworks: 1},
		{Stage: "review", Name: "accept", Type: "approval", Status: workflow.StatusPass, FinishedAt: at(born.Add(2500 * time.Millisecond))},
		{Stage: "review", Name: "pending", Type: "agent/pi", Status: workflow.StatusPending},
		{Stage: "review", Name: "running", Type: "agent/pi", Status: workflow.StatusRunning, StartedAt: at(born.Add(3 * time.Second))},
	}}}
	timeline := timingTimeline(runs[0], nil)
	if len(timeline) != 5 || timeline[0].Ms != 1250 || timeline[0].WaitMs != 250 || timeline[1].WaitMs != 0 || timeline[2].WaitMs != 500 || timeline[3].StartedAt != "" || timeline[4].WaitMs != 500 || timeline[4].FinishedAt != "" {
		t.Fatalf("timeline: %+v", timeline)
	}
	summary := summarizeWorkflowTiming("timing", 1, runs, nil)
	if summary.TotalMedianMs != 2500 || summary.Steps[0].Reworks != 1 || summary.Steps[1].Reworks != 1 {
		t.Fatalf("fractional total / names across stages: %+v", summary)
	}
}
