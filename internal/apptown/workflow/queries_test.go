package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func activityFixture(t *testing.T) (queries, *Run, ids.ID) {
	t.Helper()
	db := testDB(t)
	project := workflowTestProject(t, db, "completion", "Completion", "")
	change := admissionTestChange(t, db, project, "A change")
	run := insertStoredRun(t, db, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: project, ChangeID: change, WorkflowName: "test", WorkflowVersion: 1, Kind: "pipeline", Status: RunRunning.String(), CurrentStage: "finish", DefinitionJson: json.RawMessage(`{"kind":"pipeline","role":"working","stages":[{"name":"finish"}]}`)})
	step := insertStoredStep(t, db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "finish", Name: "accept", Type: "approval", Status: StatusPending.String()})
	return queries{db: db}, run, step
}
func activityChild(t *testing.T, db *database.DB, parent *Run, name string) (*Run, ids.ID) {
	t.Helper()
	step := insertStoredCall(t, db, parent.ID, "finish", name, 2, nil)
	child := insertStoredRun(t, db, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: parent.ProjectID, WorkflowVersion: 1, Kind: "pipeline", DefinitionJson: json.RawMessage(`{}`), WorkflowName: name, Status: RunRunning.String(), CurrentStage: "finish", ParentRunID: parent.ID, ParentStepID: step}).ID
	if err := requireRunWrite(workflowsql.New(db.SQL()).SetParentStepAttempt(context.Background(), workflowsql.SetParentStepAttemptParams{ParentStepAttempt: 2, ID: child})); err != nil {
		t.Fatal(err)
	}
	recordStoredCallEvidence(t, db, step, 2, json.RawMessage(fmt.Sprintf(`{"childRunId":%q,"workflow":%q}`, child, name)))
	run, err := readRun(context.Background(), db, child)
	if err != nil {
		t.Fatal(err)
	}
	return run, step
}

func TestWorkflowListUsesTwoReadsRegardlessOfRunCount(t *testing.T) {
	t.Parallel()
	// Three runs: a read per run already differs at three.
	for _, size := range []int{1, 3} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			t.Parallel()
			q, root, _ := activityFixture(t)
			admissions := newRunAdmissions(q.db, NewStages(nil, func(string) bool { return false }), nil)
			for n := 2; n <= size; n++ {
				name := fmt.Sprintf("bulk-%d", n)
				if _, err := admissions.Create(context.Background(), StartRequest{ProjectID: root.ProjectID, WorkflowName: name, InputsJSON: `{}`}, PreparedRun{Workflow: DefinitionRef{Name: name, Version: 1}, Spec: schema.Spec{Kind: "pipeline", Stages: []schema.Stage{{Name: "finish", Steps: []schema.Step{{Name: "review", Uses: "approval"}}}}}}); err != nil {
					t.Fatal(err)
				}
			}
			counted := &countedQueries{Handle: q.db.SQL()}
			rows, err := readWorkflowSummaries(context.Background(), counted, RunQuery{ProjectID: root.ProjectID, State: RunRunning, RootsOnly: true}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if counted.reads != 2 || len(rows) != size {
				t.Fatalf("%d runs: %d reads, %d rows", size, counted.reads, len(rows))
			}
			for _, row := range rows {
				if row.Status != RunRunning || row.State != ActivityWaiting {
					t.Fatalf("wrong activity: %+v", row)
				}
			}
			if size != 1 {
				return // the empty result does not depend on the fixture's size
			}
			counted.reads = 0
			rows, err = readWorkflowSummaries(context.Background(), counted, RunQuery{ProjectSlug: "absent"}, nil)
			if err != nil || rows == nil || len(rows) != 0 || counted.reads != 1 {
				t.Fatalf("empty result: %+v %v reads=%d", rows, err, counted.reads)
			}
		})
	}
}

func TestWorkflowListFiltersAndStableOrdering(t *testing.T) {
	t.Parallel()
	q, root, _ := activityFixture(t)
	child, _ := activityChild(t, q.db, root, "child")
	otherProject := workflowTestProject(t, q.db, "another", "Another", "")
	other := insertStoredRun(t, q.db, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: otherProject, WorkflowVersion: 1, Kind: "pipeline", DefinitionJson: json.RawMessage(`{}`), CurrentStage: "finish", WorkflowName: "another", Status: RunDone.String()}).ID
	// sql-guarantee: Tie root and child timestamps exactly to test the stable ordering key; TransitionRun always uses database now() and cannot set this shared historical time.
	completionExec(t, q.db, `UPDATE runs SET updated_at='2026-09-01T00:00:00Z'`)
	rows, err := q.list(context.Background(), RunQuery{ProjectID: root.ProjectID, RootsOnly: true})
	if err != nil || len(rows) != 1 || rows[0].ID != root.ID {
		t.Fatalf("root filter: %+v %v", rows, err)
	}
	// A summary names its run and its change by the numbers people read.
	if ch, err := ReadChangeIn(context.Background(), q.db, root.ChangeID); err != nil || root.Number <= 0 || rows[0].Number != root.Number || rows[0].ChangeNumber != ch.Number {
		t.Fatalf("summary numbers run %d change %d, want %d and the change's: %+v %v", rows[0].Number, rows[0].ChangeNumber, root.Number, ch, err)
	}
	rows, err = q.list(context.Background(), RunQuery{ProjectID: root.ProjectID, Limit: 1})
	if err != nil || len(rows) != 1 || rows[0].ID != child.ID {
		t.Fatalf("stable limit: %+v %v", rows, err)
	}
	rows, err = q.list(context.Background(), RunQuery{ProjectSlug: "another", State: RunDone})
	if err != nil || len(rows) != 1 || rows[0].ID != other || rows[0].State != "" || rows[0].ChangeNumber != 0 {
		t.Fatalf("terminal filter: %+v %v", rows, err)
	}
	for _, query := range []RunQuery{{Limit: -1}, {State: RunState(100)}} {
		if _, err := q.list(context.Background(), query); err == nil {
			t.Fatalf("invalid query: %+v", query)
		}
	}
}

func TestWorkflowActivityBatchesNestedCallsAndChecksOwnership(t *testing.T) {
	t.Parallel()
	for _, damage := range []string{"none", "foreign project", "old attempt", "wrong parent", "missing child", "malformed", "null", "array"} {
		t.Run(damage, func(t *testing.T) {
			t.Parallel()
			q, root, _ := activityFixture(t)
			child, call := activityChild(t, q.db, root, "build")
			grandchild, _ := activityChild(t, q.db, child, "inspect")
			insertStoredStep(t, q.db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: grandchild.ID, Stage: "finish", Name: "private verdict", Type: "scan", Status: StatusFail.String()})
			switch damage {
			case "foreign project":
				project := workflowTestProject(t, q.db, "private", "Private", "")
				// sql-guarantee: Move an already-linked child into a foreign project without changing its identity; run admission allocates a new identity and no run mutation reassigns project_id.
				completionExec(t, q.db, `UPDATE runs SET project_id=$1,number=number+100000 WHERE id=$2`, project, child.ID)
			case "old attempt":
				current, err := workflowsql.New(q.db.SQL()).StepByID(context.Background(), call)
				if err != nil {
					t.Fatal(err)
				}
				if err := requireAttemptWrite(workflowsql.New(q.db.SQL()).StartParentCall(context.Background(), workflowsql.StartParentCallParams{ID: call, OldStatus: current.Status, Status: current.Status, Attempt: current.Attempt, StartedAt: current.StartedAt, EvidenceJson: current.EvidenceJson})); err != nil {
					t.Fatal(err)
				}
			case "wrong parent":
				// sql-guarantee: Detach the already-linked child while leaving its call evidence intact; no existing run mutation changes parent_run_id.
				completionExec(t, q.db, `UPDATE runs SET parent_run_id=NULL WHERE id=$1`, child.ID)
			case "missing child":
				recordStoredCallEvidence(t, q.db, call, 2, json.RawMessage(`{"childRunId":"00000001-869f-7000-8000-000000000000"}`))
			case "malformed":
				recordStoredCallEvidence(t, q.db, call, 2, json.RawMessage(`{"childRunId":"not a number"}`))
			case "null", "array":
				body := "null"
				if damage == "array" {
					body = "[]"
				}
				recordStoredCallEvidence(t, q.db, call, 2, json.RawMessage(body))
			}
			counted := &countedQueries{Handle: q.db.SQL()}
			graph, err := readWorkflowActivity(context.Background(), counted, []ids.ID{root.ID})
			if err != nil {
				t.Fatal(err)
			}
			if damage == "foreign project" || damage == "old attempt" || damage == "wrong parent" {
				if graph[child.ID] != nil {
					t.Fatalf("%s entered the owned activity graph", damage)
				}
			}
			activity, err := graph.classify(root.ID, nil, make(map[ids.ID]bool))
			if counted.reads != 1 {
				t.Fatalf("nested activity made %d reads", counted.reads)
			}
			if damage != "none" {
				if err == nil || activity != (Activity{}) || strings.Contains(err.Error(), "private verdict") {
					t.Fatalf("%s exposed child activity: %+v %v", damage, activity, err)
				}
				return
			}
			if err != nil || activity.State != ActivityBlocked || activity.Detail != "private verdict (fail) (in inspect) (in build)" {
				t.Fatalf("nested state: %+v %v", activity, err)
			}
		})
	}
}

func TestWorkflowActivityRejectsCyclicCallsWithoutUnboundedReads(t *testing.T) {
	t.Parallel()
	q, root, _ := activityFixture(t)
	child, _ := activityChild(t, q.db, root, "child")
	cycle := insertStoredCall(t, q.db, child.ID, "finish", "back to root", 2, json.RawMessage(fmt.Sprintf(`{"childRunId":%q,"workflow":"root"}`, root.ID)))
	// sql-guarantee: Make the existing root its descendant's child to test bounded cycle detection; admission cannot rewrite these parent identities into a cycle.
	completionExec(t, q.db, `UPDATE runs SET parent_run_id=$1,parent_step_id=$2,parent_step_attempt=2 WHERE id=$3`, child.ID, cycle, root.ID)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := newService(q.db, allowProject(root.ProjectID), Runtime{}).Activities(ctx, []ids.ID{root.ID}); err == nil || !strings.Contains(err.Error(), "cyclic") {
		t.Fatalf("cycle was not bounded: %v", err)
	}
}

func TestWorkflowListKeepsOneDatabaseSnapshot(t *testing.T) {
	t.Parallel()
	q, root, step := activityFixture(t)
	gate, err := q.db.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = gate.Rollback() }()
	if _, err := gate.SQL().ExecContext(context.Background(), `LOCK TABLE steps IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	if err := requireAttemptWrite(workflowsql.New(gate.SQL()).RecordApproval(context.Background(), workflowsql.RecordApprovalParams{At: time.Now(), ID: step, Attempt: 0, ExpectedStatus: StatusPending.String(), Status: StatusPass.String(), EvidenceJson: json.RawMessage(`{}`)})); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	type result struct {
		rows []RunSummary
		err  error
	}
	done := make(chan result, 1)
	go func() {
		rows, err := q.list(ctx, RunQuery{ProjectID: root.ProjectID, State: RunRunning})
		done <- result{rows, err}
	}()
	dbtest.WaitForLock(t, q.db, "WITH RECURSIVE activity_tree")
	if err := requireRunWrite(workflowsql.New(gate.SQL()).TransitionRun(context.Background(), workflowsql.TransitionRunParams{At: time.Now(), ID: root.ID, ExpectedStatus: root.Status.String(), CurrentStage: root.CurrentStage, RetryRevision: root.RetryRevision, NextStatus: RunDone.String(), NextStage: "later"})); err != nil {
		t.Fatal(err)
	}
	if err := gate.Commit(); err != nil {
		t.Fatal(err)
	}
	out := <-done
	if out.err != nil || len(out.rows) != 1 || out.rows[0].Status != RunRunning || out.rows[0].State != ActivityWaiting {
		t.Fatalf("torn list snapshot: %+v %v", out.rows, out.err)
	}
	next, err := q.list(ctx, RunQuery{ProjectID: root.ProjectID, State: RunRunning})
	if err != nil || len(next) != 0 {
		t.Fatalf("new snapshot did not observe completion: %+v %v", next, err)
	}
}

func TestWorkflowActivityCancellationAndMissingRun(t *testing.T) {
	t.Parallel()
	q, root, _ := activityFixture(t)
	svc := newService(q.db, allowProject(root.ProjectID), Runtime{})
	if _, err := svc.Activities(context.Background(), []ids.ID{idstest.N(99999)}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing run: %v", err)
	}
	gate, err := q.db.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = gate.Rollback() }()
	if _, err := gate.SQL().ExecContext(context.Background(), `LOCK TABLE steps IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := svc.Activities(ctx, []ids.ID{root.ID}); done <- err }()
	dbtest.WaitForLock(t, q.db, "WITH RECURSIVE activity_tree")
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled activity query did not finish")
	}
}

func TestWorkflowActivityPreservesQueuePriorityAndCallStartup(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		steps   []workflowActivityStep
		waiting bool
		want    Activity
	}{
		{"entry queue before failed gate", []workflowActivityStep{{id: idstest.N(1), name: "build", kind: "agent/pi", state: StatusPending}, {id: idstest.N(2), name: "scan", kind: "scan", state: StatusFail}}, false, Activity{State: ActivityQueued, Detail: "build"}},
		// A review still to run may send the stage back for rework, so the
		// stage is not blocked yet; the failure already in is named.
		{"review still to run names the failed gate", []workflowActivityStep{{id: idstest.N(1), name: "review", kind: "agent/review", state: StatusPending}, {id: idstest.N(2), name: "scan", kind: "scan", state: StatusFail}}, false, Activity{State: ActivityWorking, Detail: "review next; scan (fail) so far"}},
		// The gap between one step finishing and the next starting (run
		// 507): never a person's move, never blocked.
		{"gate still to run is not waiting on you", []workflowActivityStep{{id: idstest.N(1), name: "apply-change", kind: "agent/pi", state: StatusPass}, {id: idstest.N(2), name: "boot", kind: "preview", state: StatusPending}, {id: idstest.N(3), name: "report-exists", kind: "artifact/require", state: StatusPending}, {id: idstest.N(4), name: "accept", kind: "approval", state: StatusPending}}, false, Activity{State: ActivityWorking, Detail: "boot next"}},
		{"assertions and approvals alone wait on a person", []workflowActivityStep{{id: idstest.N(1), name: "apply-change", kind: "agent/pi", state: StatusPass}, {id: idstest.N(3), name: "report-exists", kind: "artifact/require", state: StatusPending}, {id: idstest.N(4), name: "accept", kind: "approval", state: StatusPending}}, false, Activity{State: ActivityWaiting, Detail: "accept", Approval: idstest.N(4)}},
		{"review waiting for slot", []workflowActivityStep{{id: idstest.N(1), name: "review", kind: "agent/review", state: StatusPending}, {id: idstest.N(2), name: "scan", kind: "scan", state: StatusFail}}, true, Activity{State: ActivityQueued, Detail: "review — waiting for a slot on local, one ahead"}},
		{"call awaiting child creation", []workflowActivityStep{{id: idstest.N(1), name: "build", kind: "workflow/call", state: StatusRunning, evidence: []byte(`{}`)}}, false, Activity{State: ActivityWorking, Detail: "build"}},
		{"running agent wins", []workflowActivityStep{{id: idstest.N(1), name: "build", kind: "agent/pi", state: StatusPending}, {id: idstest.N(2), name: "work", kind: "agent/pi", state: StatusRunning}}, false, Activity{State: ActivityAgentWorking, Detail: "work"}},
		{"running script wins", []workflowActivityStep{{id: idstest.N(1), name: "accept", kind: "approval", state: StatusPending}, {id: idstest.N(2), name: "test", kind: "script", state: StatusRunning}}, false, Activity{State: ActivityWorking, Detail: "test"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			graph := workflowActivityGraph{idstest.N(1): {id: idstest.N(1), state: RunRunning, steps: tc.steps}}
			got, err := graph.classify(idstest.N(1), func(id ids.ID) (string, int, bool) { return "local", 1, tc.waiting && id == idstest.N(1) }, make(map[ids.ID]bool))
			if err != nil || got != tc.want {
				t.Fatalf("activity %+v %v, want %+v", got, err, tc.want)
			}
		})
	}
}

func TestWorkflowActivityDoesNotFollowAnotherRequestedRoot(t *testing.T) {
	t.Parallel()
	q, root, _ := activityFixture(t)
	other := insertStoredRun(t, q.db, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: root.ProjectID, WorkflowName: "independent", WorkflowVersion: 1, Kind: "pipeline", CurrentStage: "finish", Status: RunRunning.String(), DefinitionJson: json.RawMessage(`{}`)}).ID
	call := insertStoredStep(t, q.db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: root.ID, Stage: "finish", Name: "borrow another run", Type: "workflow/call", Status: StatusRunning.String()})
	recordStoredCallEvidence(t, q.db, call, 0, json.RawMessage(fmt.Sprintf(`{"childRunId":%q}`, other)))
	insertStoredStep(t, q.db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: other, Stage: "finish", Name: "independent review", Type: "approval", Status: StatusPending.String()})
	if activities, err := newService(q.db, allowProject(root.ProjectID), Runtime{}).Activities(context.Background(), []ids.ID{root.ID, other}); err == nil || activities != nil {
		t.Fatalf("cross-root evidence became owned activity: %+v %v", activities, err)
	}
}

func TestWorkflowQueriesRejectInvalidStoredStatesWithoutPartialResults(t *testing.T) {
	t.Parallel()
	q, root, step := activityFixture(t)
	if err := requireAttemptWrite(workflowsql.New(q.db.SQL()).RecordApproval(context.Background(), workflowsql.RecordApprovalParams{At: time.Now(), ID: step, Attempt: 0, ExpectedStatus: StatusPending.String(), Status: "unknown state", EvidenceJson: json.RawMessage(`{}`)})); err != nil {
		t.Fatal(err)
	}
	if rows, err := q.list(context.Background(), RunQuery{ProjectID: root.ProjectID}); rows != nil || !errors.Is(err, errStepState) {
		t.Fatalf("invalid step produced partial list: %+v %v", rows, err)
	}
	if err := requireAttemptWrite(workflowsql.New(q.db.SQL()).ReopenApproval(context.Background(), workflowsql.ReopenApprovalParams{ID: step, Attempt: 0, EvidenceJson: json.RawMessage(`{}`)})); err != nil {
		t.Fatal(err)
	}
	if err := requireRunWrite(workflowsql.New(q.db.SQL()).TransitionRun(context.Background(), workflowsql.TransitionRunParams{At: time.Now(), ID: root.ID, ExpectedStatus: root.Status.String(), CurrentStage: root.CurrentStage, RetryRevision: root.RetryRevision, NextStage: root.CurrentStage, NextStatus: "unknown state"})); err != nil {
		t.Fatal(err)
	}
	if rows, err := q.list(context.Background(), RunQuery{ProjectID: root.ProjectID}); rows != nil || !errors.Is(err, errRunState) {
		t.Fatalf("invalid run produced partial list: %+v %v", rows, err)
	}
}
