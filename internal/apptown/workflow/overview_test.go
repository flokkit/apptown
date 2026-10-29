package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestWorkHistoryUsesOneRootAggregateAndProjectFence(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	projectID := workflowTestProject(t, db, "completion", "Completion", "")
	changeID := insertChangeForTest(t, db, workflowsql.InsertChangeParams{ID: ids.New(time.Now()), CreatedAt: time.Now(), ProjectID: projectID, Type: "feature", Title: "A change", State: ChangeInProgress.String()})
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	run := insertStoredRun(t, db, workflowsql.InsertRunParams{ID: ids.New(start), ProjectID: projectID, ChangeID: changeID, WorkflowName: "test", WorkflowVersion: 1, Kind: "pipeline", Status: RunDone.String(), CurrentStage: "finish", CreatedAt: start, UpdatedAt: start.Add(30 * time.Second), DefinitionJson: []byte(`{"kind":"pipeline","role":"working","stages":[{"name":"finish"}]}`)})
	q := queries{db: db}
	transitionStoredChangeForTest(t, q.db, run.ChangeID, ChangeShipped, false)
	minute := time.Date(2026, 1, 1, 0, 1, 0, 0, time.UTC)
	insertStoredRun(t, q.db, workflowsql.InsertRunParams{ID: ids.New(minute), ProjectID: run.ProjectID, ChangeID: run.ChangeID, WorkflowName: "land", WorkflowVersion: 1, Kind: "pipeline", Status: RunDone.String(), CurrentStage: "finish", CreatedAt: minute, UpdatedAt: minute.Add(20 * time.Second), DefinitionJson: []byte(`{}`)})
	insertStoredRun(t, q.db, workflowsql.InsertRunParams{ID: ids.New(minute.Add(-time.Minute)), ProjectID: run.ProjectID, ChangeID: run.ChangeID, WorkflowName: "child", WorkflowVersion: 1, Kind: "pipeline", Status: RunDone.String(), CurrentStage: "finish", ParentRunID: run.ID, CreatedAt: minute.Add(-time.Minute), UpdatedAt: minute.Add(24*time.Hour - time.Minute), DefinitionJson: []byte(`{}`)})
	other := workflowTestProject(t, q.db, "private", "Private", "")
	foreign := insertStoredRun(t, q.db, workflowsql.InsertRunParams{ID: ids.New(minute.Add(-time.Minute)), ProjectID: other, ChangeID: run.ChangeID, WorkflowName: "foreign", WorkflowVersion: 1, Kind: "pipeline", Status: RunDone.String(), CurrentStage: "finish", CreatedAt: minute.Add(-time.Minute), UpdatedAt: minute.Add(48*time.Hour - time.Minute), DefinitionJson: []byte(`{}`)}).ID
	insertRunEventForTest(t, q.db, foreign, "merge.completed", nil)
	insertChangeForTest(t, q.db, workflowsql.InsertChangeParams{ID: ids.New(time.Now()), CreatedAt: time.Now(), ProjectID: other, Type: "feature", Title: "secret", State: ChangeShipped.String()})
	counted := &countedQueries{Handle: q.db.SQL()}
	history, err := readChangeHistory(context.Background(), counted, HistoryQuery{Limit: 30}, []ids.ID{run.ProjectID})
	if err != nil || len(history) != 1 || counted.reads != 1 {
		t.Fatalf("history %+v, reads %d: %v", history, counted.reads, err)
	}
	h := history[0]
	// The row names its change by the number people read, not only its id.
	if ch, err := ReadChangeIn(context.Background(), q.db, run.ChangeID); err != nil || ch.Number <= 0 || h.ChangeNumber != ch.Number {
		t.Fatalf("history names change %d, want its number: %+v %v", h.ChangeNumber, ch, err)
	}
	if h.ChangeID != run.ChangeID || h.State != ChangeShipped || h.Runs != 2 || h.CompletedRuns != 2 || h.TookS != 50 || h.Workflows != "test → land" || h.Merged || h.Project != "completion" {
		t.Fatalf("root history %+v", h)
	}
	service := newService(q.db, allowProject(run.ProjectID), Runtime{})
	visible, err := service.ChangeHistory(context.Background(), HistoryQuery{Limit: 30})
	if err != nil || len(visible) != 1 || visible[0] != h {
		t.Fatalf("visible history %+v: %v", visible, err)
	}
	insertRunEventForTest(t, q.db, run.ID, "merge.completed", nil)
	scoped, err := service.ChangeHistory(context.Background(), HistoryQuery{ProjectID: run.ProjectID, Limit: 30})
	if err != nil || len(scoped) != 1 || !scoped[0].Merged || scoped[0].Project != "" {
		t.Fatalf("project history %+v: %v", scoped, err)
	}
	if _, err := service.ChangeHistory(context.Background(), HistoryQuery{ProjectID: other, Limit: 1}); err == nil {
		t.Fatal("foreign history admitted")
	}
}

func TestWorkOverviewOpenChangesAndRunningStepsKeepTheirScope(t *testing.T) {
	t.Parallel()
	q, run, step := activityFixture(t)
	transitionStoredChangeForTest(t, q.db, run.ChangeID, ChangeInProgress, false)
	startStoredStepForTest(t, q.db, step, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	other := workflowTestProject(t, q.db, "private", "Private", "")
	private := insertChangeForTest(t, q.db, workflowsql.InsertChangeParams{ID: ids.New(time.Now()), CreatedAt: time.Now(), ProjectID: other, Type: "feature", Title: "secret", State: ChangeInProgress.String()})
	service := newService(q.db, allowProject(run.ProjectID), Runtime{})
	running, err := service.RunningSteps(context.Background())
	if err != nil || len(running) != 1 || running[0].Title != "A change" || running[0].RunID != run.ID || !running[0].StartedAt.Valid {
		t.Fatalf("running %+v: %v", running, err)
	}
	open, err := service.OpenChangesWithoutRuns(context.Background(), run.ProjectID, 10)
	if err != nil || open == nil || len(open) != 0 {
		t.Fatalf("active Change marked idle %+v: %v", open, err)
	}
	// sql-guarantee: visibility requires changing only the run Change link to a foreign or restored Change; no Workflow mutation reassigns change_id.
	completionExec(t, q.db, `UPDATE runs SET change_id=$1 WHERE id=$2`, private, run.ID)
	running, err = service.RunningSteps(context.Background())
	if err != nil || len(running) != 1 || running[0].Title != "" {
		t.Fatalf("foreign Change title leaked %+v: %v", running, err)
	}
	// sql-guarantee: visibility requires a deliberately foreign run identity while retaining its existing step and Change links; no Workflow mutation reassigns project_id.
	completionExec(t, q.db, `UPDATE runs SET change_id=$1,project_id=$2 WHERE id=$3`, run.ChangeID, other, run.ID)
	running, err = service.RunningSteps(context.Background())
	if err != nil || running == nil || len(running) != 0 {
		t.Fatalf("foreign Run leaked %+v: %v", running, err)
	}
	open, err = service.OpenChangesWithoutRuns(context.Background(), run.ProjectID, 10)
	if err != nil || len(open) != 1 || open[0].ID != run.ChangeID {
		t.Fatalf("foreign Run hid open Change %+v: %v", open, err)
	}
	if ch, err := ReadChangeIn(context.Background(), q.db, run.ChangeID); err != nil || ch.Number <= 0 || open[0].Number != ch.Number {
		t.Fatalf("open change named %d, want its number: %+v %v", open[0].Number, ch, err)
	}
	if _, err := service.OpenChangesWithoutRuns(context.Background(), other, 10); err == nil {
		t.Fatal("foreign open Changes admitted")
	}
}

func TestWorkOverviewRefusesChangedScopeDuringAuthorization(t *testing.T) {
	t.Parallel()
	q, run, step := activityFixture(t)
	other := workflowTestProject(t, q.db, "private", "Private", "")
	startStoredStepForTest(t, q.db, step, time.Time{})
	service := newService(q.db, projectAuthority{project: run.ProjectID, authorityFunc: authorityFunc(func(_ context.Context, p operationPermission) (string, error) {
		if p.Action != actionIdentify && p.ProjectID == other {
			return "", &access.Denied{Reason: "private"}
		}
		if p.Action == actionReadRun {
			// sql-guarantee: visibility requires a deliberately foreign run identity while retaining its existing step and Change links; no Workflow mutation reassigns project_id.
			completionExec(t, q.db, `UPDATE runs SET project_id=$1,number=number+100000 WHERE id=$2`, other, run.ID)
		}
		return "reader", nil
	})}, Runtime{})
	out, err := service.RunningSteps(context.Background())
	if err != nil || len(out) != 0 {
		t.Fatalf("moved Run escaped scope fence %+v: %v", out, err)
	}
}

func TestWorkOverviewReadFailuresAndBounds(t *testing.T) {
	t.Parallel()
	for _, test := range []string{"history", "open", "running", "visibility", "history limit", "history project", "open limit", "open project", "cancelled"} {
		t.Run(test, func(t *testing.T) {
			t.Parallel()
			q, run, _ := activityFixture(t)
			service := newService(q.db, allowProject(run.ProjectID), Runtime{})
			ctx := context.Background()
			var err error
			switch test {
			case "history":
				dbtest.FailNextStatement(t, q.db, "CROSS JOIN LATERAL")
				_, err = service.ChangeHistory(ctx, HistoryQuery{ProjectID: run.ProjectID, Limit: 10})
			case "open":
				dbtest.FailNextStatement(t, q.db, "SELECT c.id,c.number,c.title,c.state")
				_, err = service.OpenChangesWithoutRuns(ctx, run.ProjectID, 10)
			case "running":
				dbtest.FailNextStatement(t, q.db, "FROM steps s JOIN runs r")
				_, err = service.RunningSteps(ctx)
			case "visibility":
				service.authority = aggregateAuthority{operationAuthority: service.authority, read: func(context.Context, commandAction) ([]ids.ID, error) { return nil, database.ErrInjectedFailure }}
				_, err = service.ChangeHistory(ctx, HistoryQuery{})
				if !errors.Is(err, database.ErrInjectedFailure) {
					t.Fatalf("lost visibility failure: %v", err)
				}
			case "history limit":
				_, err = service.ChangeHistory(ctx, HistoryQuery{Limit: -1})
			case "history project":
				_, err = service.ChangeHistory(ctx, HistoryQuery{ProjectID: idstest.N(999999)})
			case "open limit":
				_, err = service.OpenChangesWithoutRuns(ctx, run.ProjectID, -1)
			case "open project":
				_, err = service.OpenChangesWithoutRuns(ctx, ids.Nil, 10)
			case "cancelled":
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				_, err = service.RunningSteps(cancelled)
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation: %v", err)
				}
			}
			if err == nil {
				t.Fatal("invalid/failed overview admitted")
			}
		})
	}
}

// Before an AppTown restart the operator asks what is executing. A run waiting
// on a person (directly, or in a called workflow) and a step queued for a
// slot survive a restart and are not executing; a claimed step is, in any
// project. The answer is about the whole instance, so it is asked at the
// instance and refused to a caller without that reach.
func TestExecutingStepsAreOnlyStepsAtWorkInEveryProject(t *testing.T) {
	t.Parallel()
	q, run, _ := activityFixture(t)
	ctx := context.Background()
	child, _ := activityChild(t, q.db, run, "land")
	insertStoredStep(t, q.db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: child.ID, Stage: "finish", Name: "accept", Type: "approval", Status: StatusPending.String()})
	insertStoredStep(t, q.db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "finish", Name: "queued", Type: "agent/pi", Status: StatusPending.String()})
	var asked []operationPermission
	service := newService(q.db, authorityFunc(func(_ context.Context, p operationPermission) (string, error) {
		asked = append(asked, p)
		return "admin", nil
	}), Runtime{})
	waiting, err := service.ExecutingSteps(ctx)
	if err != nil || waiting == nil || len(waiting) != 0 {
		t.Fatalf("a run waiting on a person read as executing: %+v %v", waiting, err)
	}
	if len(asked) != 1 || asked[0].Action != actionReadRun || !asked[0].ProjectID.IsZero() {
		t.Fatalf("not asked at the instance: %+v", asked)
	}

	other := workflowTestProject(t, q.db, "foreign", "Foreign", "")
	foreign := insertStoredRun(t, q.db, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: other, WorkflowName: "build", WorkflowVersion: 1, Kind: "pipeline", Status: RunRunning.String(), CurrentStage: "finish", DefinitionJson: json.RawMessage(`{}`)})
	agent := insertStoredStep(t, q.db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: foreign.ID, Stage: "finish", Name: "implement", Type: "agent/pi", Status: StatusPending.String()})
	startStoredStepForTest(t, q.db, agent, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	executing, err := service.ExecutingSteps(ctx)
	if err != nil || len(executing) != 1 {
		t.Fatalf("executing %+v: %v", executing, err)
	}
	if got := executing[0]; got.StepID != agent || got.RunID != foreign.ID || got.Project != "foreign" || got.Name != "implement" || got.Type != "agent/pi" || !got.StartedAt.Valid {
		t.Fatalf("executing step %+v", got)
	}

	denied := newService(q.db, authorityFunc(func(_ context.Context, p operationPermission) (string, error) {
		if p.Action != actionIdentify && p.ProjectID.IsZero() {
			return "", &access.Denied{Reason: "not instance-wide"}
		}
		return "member", nil
	}), Runtime{})
	if out, err := denied.ExecutingSteps(ctx); out != nil || err == nil {
		t.Fatalf("a caller without instance reach was answered: %+v %v", out, err)
	}
}
