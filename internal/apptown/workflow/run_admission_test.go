package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func runAdmissionFixture(t *testing.T) (runAdmissions, StartRequest, PreparedRun) {
	t.Helper()
	db := testDB(t)
	project := workflowTestProject(t, db, "admission", "Admission", "")
	change := admissionTestChange(t, db, project, "work")
	request := StartRequest{ProjectID: project, ChangeID: change, WorkflowName: "test", InputsJSON: `{"precise":9007199254740993}`, RequestedBy: "operator"}
	def := PreparedRun{Workflow: DefinitionRef{Name: "test", Version: 2, Origin: "library/test@2"}, Spec: schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "first", Steps: []schema.Step{{Name: "accept", Uses: "approval"}}}}}}
	return newRunAdmissions(db, NewStages(nil, func(kind string) bool { return kind == "script" }), nil), request, def
}

// Admission storage tests need a change identity without exercising the
// higher-level change creator and its unrelated workspace/audit collaborators.
func admissionTestChange(t *testing.T, db *database.DB, project ids.ID, title string) ids.ID {
	t.Helper()
	return insertChangeForTest(t, db, workflowsql.InsertChangeParams{ID: ids.New(time.Now()), CreatedAt: time.Now(),
		ProjectID: project, Type: "feature", Title: title, AcceptanceJson: json.RawMessage(`{}`),
		State: ChangeInProgress.String(), TitleOwnership: "human",
	})
}

func insertChangeForTest(t *testing.T, db *database.DB, in workflowsql.InsertChangeParams) ids.ID {
	t.Helper()
	if len(in.AcceptanceJson) == 0 {
		in.AcceptanceJson = json.RawMessage(`{}`)
	}
	if in.State == "" {
		in.State = ChangeInProgress.String()
	}
	if in.ID.IsZero() {
		in.ID = ids.New(time.Now())
	}
	number, err := workflowsql.New(db.SQL()).NextChangeNumber(context.Background(), in.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	in.Number = number
	if in.TitleOwnership == "" {
		in.TitleOwnership = "human"
	}
	change, err := workflowsql.New(db.SQL()).InsertChange(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	return change.ID
}

func insertRunEventForTest(t *testing.T, db *database.DB, run ids.ID, kind string, payload json.RawMessage) ids.ID {
	t.Helper()
	if payload == nil {
		payload = json.RawMessage(`{}`)
	}
	if changed, err := workflowsql.New(db.SQL()).InsertRunEvent(context.Background(), workflowsql.InsertRunEventParams{ID: ids.New(time.Now().UTC()), RunID: run, At: time.Now().UTC(), Kind: kind, PayloadJson: payload}); err != nil || changed != 1 {
		t.Fatalf("insert run event: changed=%d %v", changed, err)
	}
	events := storedRunEvents(t, db, run)
	if len(events) == 0 {
		t.Fatalf("run %d has no events", run)
	}
	return events[len(events)-1].ID
}

func insertTextArtifactForTest(t *testing.T, db *database.DB, run ids.ID, kind, name, ref, body string, at time.Time) ids.ID {
	t.Helper()
	if at.IsZero() {
		at = time.Now().UTC()
	}
	artifact, err := workflowsql.New(db.SQL()).InsertTextArtifact(context.Background(), workflowsql.InsertTextArtifactParams{ID: ids.New(at), RunID: run, Kind: kind, Name: name, Ref: ref, Body: body, CreatedAt: at})
	if err != nil {
		t.Fatal(err)
	}
	return artifact.ID
}

func insertBinaryArtifactForTest(t *testing.T, db *database.DB, run ids.ID, kind, name, ref string, body []byte, at time.Time) ids.ID {
	t.Helper()
	if at.IsZero() {
		at = time.Now().UTC()
	}
	artifact, err := workflowsql.New(db.SQL()).InsertBinaryArtifact(context.Background(), workflowsql.InsertBinaryArtifactParams{ID: ids.New(at), RunID: run, Kind: kind, Name: name, Ref: ref, BodyBytes: body, CreatedAt: at})
	if err != nil {
		t.Fatal(err)
	}
	return artifact.ID
}

func transitionStoredRunForTest(t *testing.T, db *database.DB, id ids.ID, nextStage string, next RunState) {
	t.Helper()
	run, err := readRun(context.Background(), db, id)
	if err != nil {
		t.Fatal(err)
	}
	if nextStage == "" {
		nextStage = run.CurrentStage
	}
	changed, err := workflowsql.New(db.SQL()).TransitionRun(context.Background(), workflowsql.TransitionRunParams{At: time.Now(), NextStage: nextStage, NextStatus: next.String(), ID: id, ExpectedStatus: run.Status.String(), CurrentStage: run.CurrentStage, RetryRevision: run.RetryRevision})
	if err != nil || changed != 1 {
		t.Fatalf("transition stored run: changed=%d %v", changed, err)
	}
}

func invalidateStoredRunForTest(t *testing.T, db *database.DB, id ids.ID) {
	t.Helper()
	run, err := readRun(context.Background(), db, id)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := workflowsql.New(db.SQL()).InvalidateRunDecision(context.Background(), workflowsql.InvalidateRunDecisionParams{At: time.Now(), ID: id, CurrentStage: run.CurrentStage, RetryRevision: run.RetryRevision})
	if err != nil || changed != 1 {
		t.Fatalf("invalidate stored run: changed=%d %v", changed, err)
	}
}

func transitionStoredStepForTest(t *testing.T, db *database.DB, id ids.ID, next StepState) {
	t.Helper()
	step, err := readStep(context.Background(), db, id)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := workflowsql.New(db.SQL()).RecordApproval(context.Background(), workflowsql.RecordApprovalParams{At: time.Now(), Status: next.String(), EvidenceJson: step.Evidence, ID: id, Attempt: step.Attempt, ExpectedStatus: step.Status.String()})
	if err != nil || changed != 1 {
		t.Fatalf("transition stored step: changed=%d %v", changed, err)
	}
}

func startStoredStepForTest(t *testing.T, db *database.DB, id ids.ID, at time.Time) {
	t.Helper()
	step, err := readStep(context.Background(), db, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := requireAttemptWrite(workflowsql.New(db.SQL()).StartParentCall(context.Background(), workflowsql.StartParentCallParams{Status: StatusRunning.String(), StartedAt: sql.NullTime{Time: at, Valid: !at.IsZero()}, EvidenceJson: step.Evidence, ID: id, OldStatus: step.Status.String(), Attempt: step.Attempt})); err != nil {
		t.Fatal(err)
	}
}

func transitionStoredChangeForTest(t *testing.T, db *database.DB, id ids.ID, next ChangeState, closed bool) {
	t.Helper()
	change, err := readChange(context.Background(), db, id)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := workflowsql.New(db.SQL()).TransitionChange(context.Background(), workflowsql.TransitionChangeParams{At: time.Now(), State: next.String(), RunID: change.RunID, Branch: change.Branch, Closed: closed, ID: id, ProjectID: change.ProjectID, OldState: change.State.String(), OldRunID: change.RunID})
	if err != nil || changed != 1 {
		t.Fatalf("transition stored change: changed=%d %v", changed, err)
	}
}

func TestRunAdmissionRequiresOwnershipReadinessAndRelationshipWrites(t *testing.T) {
	t.Parallel()
	for _, record := range []string{"inference owner", "readiness", "landing relationship", "child attempt"} {
		t.Run(record, func(t *testing.T) {
			t.Parallel()
			s, req, def := runAdmissionFixture(t)
			db := s.records.db
			ctx := context.Background()
			before := 0
			table, condition, event := "run_inference_owners", "true", "INSERT"
			switch record {
			case "readiness":
				table = "run_starts"
			case "landing relationship":
				parent, err := s.Create(ctx, req, def)
				if err != nil {
					t.Fatal(err)
				}
				transitionStoredRunForTest(t, db, parent.ID, "", RunDone)
				req.AcceptedRunID = parent.ID
				def.Spec.Role = "landing"
				table, event = "workflow_landings", "UPDATE"
				before = 1
			case "child attempt":
				def.Spec.Stages[0].Steps = []schema.Step{{Name: "call", Uses: "workflow/call"}}
				parent, err := s.Create(ctx, req, def)
				if err != nil {
					t.Fatal(err)
				}
				if err := s.Prepared(ctx, parent.ID); err != nil {
					t.Fatal(err)
				}
				step := admittedStepID(t, queries{db: db}, parent, "call")
				req.ParentRunID, req.ParentStepID = parent.ID, step
				table, event, condition = "runs", "UPDATE", "NEW.parent_step_attempt>0"
				before = 1
			}
			completionExec(t, db, `CREATE FUNCTION veto_admission() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF `+condition+` THEN RETURN NULL; END IF; RETURN NEW; END $$`)
			completionExec(t, db, fmt.Sprintf(`CREATE TRIGGER veto_admission BEFORE %s ON %s FOR EACH ROW EXECUTE FUNCTION veto_admission()`, event, table))
			if run, err := s.Create(ctx, req, def); run != nil || err == nil {
				t.Fatalf("missing %s published run: %+v %v", record, run, err)
			}
			wantRunCount(t, db, before, nil)
			wantRunCount(t, db, 0, func(r *Run) bool { return !r.ParentRunID.IsZero() })
			if before == 0 {
				wantEventCount(t, db, 0, anyEvent)
				wantEqual(t, "change run", storedChange(t, db, req.ChangeID).RunID, ids.Nil)
			}
		})
	}
}
func TestRunAdmissionCancellationReleasesProjectLockWait(t *testing.T) {
	t.Parallel()
	s, req, def := runAdmissionFixture(t)
	db := s.records.db
	gate, err := db.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = gate.Rollback() }()
	if _, err := workflowsql.New(gate.SQL()).ProjectForUpdate(context.Background(), req.ProjectID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := s.Create(ctx, req, def); done <- err }()
	dbtest.WaitForLock(t, db, "-- name: ProjectForUpdate")
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("admission ignored cancelled lock wait")
	}
	wantRunCount(t, db, 0, nil)
}
func TestRunAdmissionPreservesExactInputsAndRejectsInvalidPlans(t *testing.T) {
	t.Parallel()
	s, req, def := runAdmissionFixture(t)
	ctx := context.Background()
	for _, invalid := range []string{"null", "[]", "bad"} {
		bad := req
		bad.InputsJSON = invalid
		if run, err := s.Create(ctx, bad, def); run != nil || err == nil {
			t.Fatalf("invalid inputs %s: %+v %v", invalid, run, err)
		}
	}
	invalid := def
	invalid.Events = []StartEvent{{Kind: "external.fact", Payload: []byte("broken")}}
	if run, err := s.Create(ctx, req, invalid); run != nil || err == nil {
		t.Fatalf("invalid receipt: %+v %v", run, err)
	}
	run, err := s.Create(ctx, req, def)
	if err != nil {
		t.Fatal(err)
	}
	stored := storedRun(t, s.records.db, run.ID)
	wantJSONField(t, "inputs", json.RawMessage(stored.InputsJSON), "precise", "9007199254740993")
	wantJSONField(t, "definition", json.RawMessage(stored.DefinitionJSON), "v", "1")
	wantInferenceOwner(t, s.records.db, run.ID, "operator")
}

var _ database.Queryer = (*database.Tx)(nil)

func TestRunReadinessRequiresItsReceipt(t *testing.T) {
	t.Parallel()
	admissions, req, def := runAdmissionFixture(t)
	ctx := context.Background()
	run, err := admissions.Create(ctx, req, def)
	if err != nil {
		t.Fatal(err)
	}
	db := admissions.records.db
	completionExec(t, db, `CREATE FUNCTION veto_readiness_receipt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.kind='run.ready' THEN RETURN NULL; END IF; RETURN NEW; END $$`)
	completionExec(t, db, `CREATE TRIGGER veto_readiness_receipt BEFORE INSERT ON run_events FOR EACH ROW EXECUTE FUNCTION veto_readiness_receipt()`)
	if err := admissions.Prepared(ctx, run.ID); err == nil {
		t.Fatal("published readiness without a receipt")
	}
	wantRunReady(t, db, run.ID, false)
	wantRunEventCount(t, db, run.ID, 0, ofKind("run.ready"))
	completionExec(t, db, `DROP TRIGGER veto_readiness_receipt ON run_events`)
	if err := admissions.Prepared(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if err := admissions.Prepared(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	wantRunEventCount(t, db, run.ID, 1, ofKind("run.ready"))
}

func TestRunReadinessRequiresItsPersistedFlagBeforeDispatch(t *testing.T) {
	t.Parallel()
	admissions, req, def := runAdmissionFixture(t)
	db := admissions.records.db
	completionExec(t, db, `CREATE FUNCTION veto_readiness_flag() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NULL; END $$`)
	completionExec(t, db, `CREATE TRIGGER veto_readiness_flag BEFORE UPDATE ON run_starts FOR EACH ROW EXECUTE FUNCTION veto_readiness_flag()`)
	dispatched := false
	starter := newStarter(db, admissions.records.stages, nil, startRuntime{
		Source:   func(context.Context, StartRequest) (StartSource, error) { return StartSource{}, nil },
		Resolve:  func(context.Context, StartRequest, StartSource) (PreparedRun, error) { return def, nil },
		Prepare:  func(context.Context, *Run) error { return nil },
		Dispatch: func(context.Context, *Run) error { dispatched = true; return nil },
	}, clocked{})
	run, err := starter.Start(context.Background(), req)
	if run == nil || err == nil || dispatched {
		t.Fatalf("unready start dispatched: run=%+v err=%v dispatched=%v", run, err, dispatched)
	}
	wantRunReady(t, db, run.ID, false)
	wantEventCount(t, db, 0, ofKind("run.ready"))
	completionExec(t, db, `DROP TRIGGER veto_readiness_flag ON run_starts`)
	if err := starter.Prepare(context.Background(), run.ID); err != nil {
		t.Fatal(err)
	}
	if err := admissions.Prepared(context.Background(), run.ID); err != nil {
		t.Fatal(err)
	}
	wantRunEventCount(t, db, run.ID, 1, ofKind("run.ready"))
	dbtest.FailNextStatement(t, db, "-- name: RunStartByID")
	if err := admissions.Prepared(context.Background(), run.ID); err == nil {
		t.Fatal("failed idempotence observation reported success")
	}
	if err := admissions.Prepared(context.Background(), idstest.N(999999)); err == nil {
		t.Fatal("missing readiness row reported success")
	}
}

// A replay with a durable identity (an accepted landing, a called child)
// finds its run in one read, and only within its project.
func TestWorkflowReplayLookupUsesOneRead(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	project := workflowTestProject(t, db, "replay", "Replay", "")
	working := insertStoredRun(t, db, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: project, WorkflowName: "working", WorkflowVersion: 1, Kind: "lifecycle", CurrentStage: "build", DefinitionJson: []byte(`{}`), Status: RunRunning.String()}).ID
	child := insertStoredRun(t, db, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: project, WorkflowName: "child", WorkflowVersion: 2, Kind: "lifecycle", CurrentStage: "build", ParentRunID: working, ParentStepID: idstest.N(7), DefinitionJson: []byte(`{}`), Status: RunRunning.String()}).ID
	if changed, err := workflowsql.New(db.SQL()).SetParentStepAttempt(context.Background(), workflowsql.SetParentStepAttemptParams{ParentStepAttempt: 3, ID: child}); err != nil || changed != 1 {
		t.Fatalf("child attempt: changed=%d %v", changed, err)
	}
	if _, err := workflowsql.New(db.SQL()).EnsureLanding(context.Background(), working); err != nil {
		t.Fatal(err)
	}
	changed, err := workflowsql.New(db.SQL()).AttachAcceptedLanding(context.Background(), workflowsql.AttachAcceptedLandingParams{LandingRunID: child, WorkRunID: working})
	if err != nil || changed != 1 {
		t.Fatalf("attach replay landing: changed=%d %v", changed, err)
	}
	ctx := context.Background()
	for _, req := range []StartRequest{{AcceptedRunID: working, ProjectID: project}, {ParentRunID: working, ParentStepID: idstest.N(7), ParentStepAttempt: 2, ProjectID: project}} {
		counted := &countedQueries{Handle: db.SQL()}
		run, err := existingStartedRun(ctx, counted, req)
		if err != nil || run == nil || run.ID != child || run.WorkflowVersion != 2 || counted.reads != 1 {
			t.Fatalf("replay lookup: %+v, reads=%d: %v", run, counted.reads, err)
		}
		req.ProjectID = idstest.N(999999)
		if run, err := existingStartedRun(ctx, db, req); run != nil || err != nil {
			t.Fatalf("cross-project replay: %+v %v", run, err)
		}
	}
}
