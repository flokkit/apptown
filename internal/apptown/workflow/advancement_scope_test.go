package workflow

import (
	"context"
	"errors"
	"testing"

	"github.com/flokkit/apptown/internal/ids/idstest"
)

type advancementScopeChecks struct {
	refresh func(context.Context, *Run) error
}

func (c advancementScopeChecks) RefreshLaw(ctx context.Context, run *Run) error {
	return c.refresh(ctx, run)
}
func (c advancementScopeChecks) Merge(context.Context, *Run) (MergeResult, error) {
	return MergeResult{Allowed: true}, nil
}

func TestAdvanceServiceDoesNotPublishMovedProjectResults(t *testing.T) {
	t.Parallel()
	for _, conflict := range []bool{true, false} {
		name := "blocked checks"
		if conflict {
			name = "stale fallback"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			q, run, _ := activityFixture(t)
			storedReadyStart(t, q.db, run.ID)
			other := workflowTestProject(t, q.db, "other", "Other", "")
			checks := advancementScopeChecks{refresh: func(context.Context, *Run) error {
				// sql-guarantee: No owner mutation rehomes an existing run; move its project during refresh to fence both advancement and fallback publication.
				completionExec(t, q.db, `UPDATE runs SET project_id=$1,number=number+100000 WHERE id=$2`, other, run.ID)
				if conflict {
					return ErrRunMoved
				}
				return nil
			}}
			owner := newAdvancer(q.db, advanceRuntime{checks: checks})
			service := newService(q.db, allowProject(run.ProjectID), Runtime{Advancement: owner, Starter: newStarter(q.db, stages{}, nil, startRuntime{}, clocked{})})
			out, err := service.Advance(context.Background(), run.ID)
			if err == nil || out != nil {
				t.Fatalf("moved project's result escaped: %+v %v", out, err)
			}
			wantEqual(t, "run project", wantRunState(t, q.db, run.ID, RunRunning).ProjectID, other)
			wantEventCount(t, q.db, 0, ofKind("run.completed", "stage.entered"))
		})
	}
}

func TestAdvanceFallbackRetainsExpectedRunIdentity(t *testing.T) {
	t.Parallel()
	for _, change := range []string{"project", "change", "parent", "parent step", "parent attempt", "same scope"} {
		t.Run(change, func(t *testing.T) {
			t.Parallel()
			current := &Run{ID: idstest.N(1), ProjectID: idstest.N(2), ChangeID: idstest.N(3), ParentRunID: idstest.N(4), ParentStepID: idstest.N(5), ParentStepAttempt: 6, Status: RunRunning, CurrentStage: "new"}
			expected := *current
			expected.CurrentStage = "old"
			switch change {
			case "project":
				current.ProjectID = idstest.N(999999)
			case "change":
				current.ChangeID = idstest.N(999999)
			case "parent":
				current.ParentRunID = idstest.N(999999)
			case "parent step":
				current.ParentStepID = idstest.N(999999)
			case "parent attempt":
				current.ParentStepAttempt++
			}
			w := advanceEngine{records: &workflowTestRecords{run: current}}
			// Exercise the public stale-observation path through the same engine.
			expected.DefinitionJSON = `{"stages":[{"name":"old"}]}`
			w.refreshLaw = func(context.Context, *Run) error { return ErrRunMoved }
			out, err := w.advanceRun(context.Background(), &expected, "alice")
			if change == "same scope" {
				if err != nil || out == nil || out.Advanced || out.Stage != "new" {
					t.Fatalf("same-scope observation: %+v %v", out, err)
				}
			} else if !errors.Is(err, ErrRunMoved) || out != nil {
				t.Fatalf("foreign identity escaped: %+v %v", out, err)
			}
		})
	}
}
