package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
)

func TestWorkflowRecordsPreserveProjectionAndLocks(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	// Ids with every bit a v7 id may set: projection keeps them exact.
	runID := idstest.Parse(t, "ffffffff-ffff-7fff-bfff-fffffffffffe")
	stepID := idstest.Parse(t, "ffffffff-ffff-7fff-bfff-ffffffffffff")
	pid := workflowTestProject(t, db, "records", "Records", "")
	// sql-guarantee: Seed an exact historical run for identity projection; InsertRun stamps its own creation time.
	completionExec(t, db, `INSERT INTO runs(id,number,project_id,workflow_name,workflow_version,kind,current_stage,definition_json,inputs_json,retry_revision,parent_step_attempt,created_at,updated_at)
		VALUES (?,1,?,'pinned',7,'lifecycle','build','{"number":9007199254740993}','{"input":9007199254740993}',3,4,'2020-01-02T03:04:05.123456+11:00','2020-01-02T03:04:05.123456+11:00')`, runID, pid)
	// sql-guarantee: Seed exact historical stamps; InsertStageStep has no timestamp parameters.
	completionExec(t, db, `INSERT INTO steps(id,run_id,stage,name,type,config_json,evidence_json,status,attempt,override_by,override_reason,started_at,finished_at)
		VALUES (?,?,'build','verify','tests','{"value":9007199254740993}','{"number":9007199254740993}','pass',5,'reviewer','accepted exception','2020-01-02T03:04:05.123456+11:00','2020-01-02T03:04:06.123456+11:00')`, stepID, runID)
	ctx := context.Background()
	run, err := ReadRunIn(ctx, db, runID)
	if err != nil || run.ID != runID || run.CreatedAt != "2020-01-01T16:04:05.123456Z" || run.UpdatedAt != run.CreatedAt || run.RetryRevision != 3 || run.ParentStepAttempt != 4 || !strings.Contains(run.DefinitionJSON, "9007199254740993") || !strings.Contains(run.InputsJSON, "9007199254740993") {
		t.Fatalf("run projection lost identity, time or JSON precision: %+v %v", run, err)
	}
	step, err := ReadStepIn(ctx, db, stepID)
	if err != nil || step.ID != stepID || step.Attempt != 5 || step.OwnedAttempt() != 0 || step.OverrideReason != "accepted exception" || string(step.Config) != `{"value":9007199254740993}` || string(step.Evidence) != `{"number":9007199254740993}` {
		t.Fatalf("step projection lost data or conferred ownership: %+v %v", step, err)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	counted := &countedQueries{Handle: tx.SQL()}
	lockedRun, err := runRecord(workflowsql.New(counted.SQL()).RunForUpdate(context.Background(), runID))
	if err != nil || !reflect.DeepEqual(lockedRun, run) || counted.reads != 1 {
		t.Fatalf("locked run projection or round trips: %+v %v, reads=%d", lockedRun, err, counted.reads)
	}
	lockedStep, err := stepResult(workflowsql.New(tx.SQL()).StepForUpdate(ctx, workflowsql.StepForUpdateParams{ID: stepID}))
	if err != nil || !reflect.DeepEqual(lockedStep, step) {
		t.Fatalf("locked step differs from ordinary read: %+v %v", lockedStep, err)
	}
	// A second transaction cannot claim the same rows while these reads hold locks.
	// sql-guarantee: a NOWAIT row-lock probe is the observation; it proves whether the owner still holds the row lock.
	for _, query := range []string{`SELECT id FROM runs WHERE id=? FOR UPDATE NOWAIT`, `SELECT id FROM steps WHERE id=? FOR UPDATE NOWAIT`} {
		id := runID
		if strings.Contains(query, "steps") {
			id = stepID
		}
		var got int64
		if err := db.QueryRow(query, id).Scan(&got); err == nil || !strings.Contains(err.Error(), "55P03") {
			t.Fatalf("read did not retain its lock: %s: %v", query, err)
		}
	}
	// sql-guarantee: Edit only an existing override's reason inside the read transaction; OverrideStep rejects an already overridden row.
	if _, err := tx.Exec(`UPDATE steps SET override_reason='uncommitted' WHERE id=?`, stepID); err != nil {
		t.Fatal(err)
	}
	inside, err := stepResult(workflowsql.New(tx.SQL()).StepForUpdate(ctx, workflowsql.StepForUpdateParams{ID: stepID}))
	if err != nil || inside.OverrideReason != "uncommitted" {
		t.Fatalf("lost transaction-local write: %+v %v", inside, err)
	}
	outside, err := ReadStepIn(ctx, db, stepID)
	if err != nil || outside.OverrideReason != "accepted exception" {
		t.Fatalf("leaked uncommitted write: %+v %v", outside, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if missing, err := ReadRunIn(ctx, db, idstest.N(999999)); missing != nil || !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing run: %+v %v", missing, err)
	}
	if missing, err := ReadStepIn(ctx, db, idstest.N(999999)); missing != nil || !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing step: %+v %v", missing, err)
	}
	dbtest.FailNextStatement(t, db, "FROM runs WHERE id")
	if got, err := ReadRunIn(ctx, db, runID); got != nil || err == nil {
		t.Fatalf("read failure ignored: %+v %v", got, err)
	}
}

func TestWorkflowStageReadsAreBulkAndConsistent(t *testing.T) {
	t.Parallel()
	specSteps := make([]schema.Step, 0, 12)
	for i := 1; i <= 12; i++ {
		specSteps = append(specSteps, schema.Step{Name: fmt.Sprintf("check-%d", i), Uses: "script"})
	}
	q, run := admitWorkflowSpec(t, schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{
		{Name: "build", Steps: specSteps},
		{Name: "later", Steps: []schema.Step{{Name: "not-yet", Uses: "script"}}},
	}})
	db := q.db
	// The read must exclude an existing row from another stage. Keep this
	// projection fixture independent of advancement and its lifecycle effects.
	later, err := workflowsql.New(db.SQL()).InsertStageStep(context.Background(), workflowsql.InsertStageStepParams{ID: ids.New(time.Now()),
		RunID: run.ID, Stage: "later", Name: "not-yet", Type: "script", ConfigJson: json.RawMessage(`{}`), Status: StatusPending.String(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if s := storedStep(t, db, later.ID); s.RunID != run.ID || s.Stage != "later" || s.Name != "not-yet" || s.FinishedAt.Valid {
		t.Fatalf("later step: %+v", s)
	}
	tx, err := db.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	counted := &countedQueries{Handle: tx.SQL()}
	steps, err := stepRecords(workflowsql.New(counted.SQL()).StageStepsForUpdate(context.Background(), workflowsql.StageStepsForUpdateParams{RunID: run.ID, Stage: "build"}))
	if err != nil || len(steps) != 12 || counted.reads != 1 {
		t.Fatalf("bulk stage: %d steps, %d reads, %v", len(steps), counted.reads, err)
	}
	ordinary, err := ReadStageStepsIn(context.Background(), db, run.ID, "build")
	if err != nil || !reflect.DeepEqual(ordinary, steps) {
		t.Fatalf("stage projections differ: %v", err)
	}
	for i, step := range steps {
		if step.OwnedAttempt() != 0 || (i > 0 && step.ID.Compare(steps[i-1].ID) <= 0) {
			t.Fatalf("invalid stage ordering/ownership: %+v", step)
		}
		var id int64
		// sql-guarantee: a NOWAIT row-lock probe is the observation; it proves whether the owner still holds the row lock.
		if err := db.QueryRow(`SELECT id FROM steps WHERE id=? FOR UPDATE NOWAIT`, step.ID).Scan(&id); err == nil || !strings.Contains(err.Error(), "55P03") {
			t.Fatalf("step %d not locked: %v", step.ID, err)
		}
	}
	if empty, err := ReadStageStepsIn(context.Background(), db, run.ID, "missing"); empty != nil || err != nil {
		t.Fatalf("empty stage: %+v %v", empty, err)
	}
	dbtest.FailNextStatement(t, db, "FROM steps WHERE run_id")
	if got, err := ReadStageStepsIn(context.Background(), db, run.ID, "build"); got != nil || err == nil {
		t.Fatalf("partial list on error: %+v %v", got, err)
	}
}

func TestWorkStageSelectionDoesNotTreatEmptyStageAsAllSteps(t *testing.T) {
	t.Parallel()
	q, run, _ := activityFixture(t)
	// Empty stage names cannot be admitted; this storage read must still select
	// that persisted shape exactly instead of interpreting it as all stages.
	empty, err := workflowsql.New(q.db.SQL()).InsertStageStep(context.Background(), workflowsql.InsertStageStepParams{ID: ids.New(time.Now()),
		RunID: run.ID, Stage: "", Name: "unnamed-stage", Type: "approval", ConfigJson: json.RawMessage(`{}`), Status: StatusPending.String(),
	})
	if err != nil {
		t.Fatal(err)
	}
	counted := &countedQueries{Handle: q.db.SQL()}
	steps, err := ReadStageStepsIn(context.Background(), counted, run.ID, "")
	if err != nil || len(steps) != 1 || steps[0].ID != empty.ID || counted.reads != 1 {
		t.Fatalf("exact stage selection: %+v %v reads=%d", steps, err, counted.reads)
	}
	all, err := readSteps(context.Background(), q.db, run.ID)
	if err != nil || len(all) != 2 {
		t.Fatalf("whole run: %+v %v", all, err)
	}
}
func TestWorkNoticeChecksItsReceiptAndKeepsExactJSON(t *testing.T) {
	t.Parallel()
	q, run, _ := activityFixture(t)
	ctx := context.Background()
	in := Notice{RunID: run.ID, Kind: "adapter.note", Payload: json.RawMessage(`{"number":9007199254740993}`), At: time.Now()}
	malformed := in
	malformed.Payload = json.RawMessage(`{`)
	if err := RecordNotice(ctx, q.db, malformed); err == nil {
		t.Fatal("invalid JSON accepted")
	}
	completionExec(t, q.db, `CREATE FUNCTION veto_notice() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NULL; END $$`)
	completionExec(t, q.db, `CREATE TRIGGER veto_notice BEFORE INSERT ON run_events FOR EACH ROW EXECUTE FUNCTION veto_notice()`)
	if err := RecordNotice(ctx, q.db, in); err == nil {
		t.Fatal("suppressed narration returned success")
	}
	wantEventCount(t, q.db, 0, anyEvent)
	completionExec(t, q.db, `DROP TRIGGER veto_notice ON run_events`)
	if err := RecordNotice(ctx, q.db, in); err != nil {
		t.Fatal(err)
	}
	wantEventCount(t, q.db, 1, kindWith("adapter.note", "number", "9007199254740993"))
	wantEqual(t, "retry revision", wantRunState(t, q.db, run.ID, RunRunning).RetryRevision, 0)
}
