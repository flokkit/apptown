package changeactions_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/attention"
	"github.com/flokkit/apptown/internal/apptown/changeactions"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/workflow"
)

// Any unreadable schedule keeps both aggregate answers unknown, regardless of
// the order in which healthy schedules are read.
func TestJanitorScheduleProblemsSurviveHealthyRows(t *testing.T) {
	t.Parallel()
	next := time.Date(2026, 11, 15, 6, 0, 0, 0, time.UTC)
	discovery := changeactions.ProjectSchedule{Standing: true, DiscoversTargets: true}
	discovery.Source.Workflow = "discover"
	update := changeactions.ProjectSchedule{Standing: true, Next: next}
	update.Source.Workflow = "update"
	for _, problem := range []struct {
		name string
		row  changeactions.ProjectSchedule
	}{
		{"definition", changeactions.ProjectSchedule{Problem: "workflow unavailable"}},
		{"discovery eligibility", changeactions.ProjectSchedule{Standing: true, DiscoversTargets: true, Problem: "eligibility unavailable"}},
		{"update admission", changeactions.ProjectSchedule{Standing: true, Problem: "queue unavailable"}},
	} {
		for _, first := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/problem first=%t", problem.name, first), func(t *testing.T) {
				t.Parallel()
				rows := []changeactions.ProjectSchedule{discovery, update, problem.row}
				if first {
					rows = []changeactions.ProjectSchedule{problem.row, discovery, update}
				}
				got := (changeactions.ProjectSchedules{Schedules: rows}).Janitor()
				if got.ScanNow != "Not known: "+problem.row.Problem || got.NextReason != "Update time not known: "+problem.row.Problem {
					t.Fatalf("problem lost from aggregate: %+v", got)
				}
			})
		}
	}
}

type failingQueueDefinition struct {
	*adoption.Service
	fail bool
}

func (d *failingQueueDefinition) WorkflowVersion(ctx context.Context, source adoption.Source, name string) (int, string, error) {
	if d.fail {
		return 0, "", fmt.Errorf("workflow definition unavailable")
	}
	return d.Service.WorkflowVersion(ctx, source, name)
}

// Queue enumeration must distinguish an unreadable adoption from a readable
// definition without a standing trigger, including the attention read.
func TestQueueDefinitionFailureStaysUnknownForCallers(t *testing.T) {
	t.Parallel()
	w := newScheduleWorld(t)
	w.adoptScheduled(t, "clock", "0 * * * *")
	definitions := &failingQueueDefinition{Service: w.adoption}
	directory := identity.New(w.schedules.DB, identity.Config{Projects: project.NewCatalogue(w.schedules.DB)})
	authority := access.Checked{Authority: directory, Projects: w.schedules.Projects}
	work := workflow.New(t.Context(), w.schedules.DB, workflow.Dependencies{Authority: authority, JanitorDefinitions: definitions, Standing: w.schedules.Standing})
	t.Cleanup(work.Stop)
	if queues, err := work.Queues(w.engine, w.project.ID); err != nil || len(queues) != 0 {
		t.Fatalf("non-standing definition: %+v %v", queues, err)
	}
	definitions.fail = true
	if queues, err := work.Queues(w.engine, w.project.ID); err == nil || !strings.Contains(err.Error(), "workflow definition unavailable") {
		t.Errorf("definition failure returned successful queues: %+v %v", queues, err)
	}
	if snapshot, err := (attention.Scans{Work: work, Projects: w.schedules.Projects}).ProjectSnapshot(w.engine, w.project.ID); err == nil || !strings.Contains(err.Error(), "workflow definition unavailable") {
		t.Errorf("attention lost definition failure: %+v %v", snapshot, err)
	}
	rows, err := work.JanitorProjects(w.engine)
	if err != nil || len(rows) != 1 || rows[0].ProjectID != w.project.ID || !strings.Contains(rows[0].Problem, "workflow definition unavailable") {
		t.Fatalf("portfolio must keep unknown on the project row: %+v %v", rows, err)
	}
}
