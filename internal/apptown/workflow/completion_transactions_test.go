package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// These fixtures exercise private persistence rules: stale relationships,
// exact identifiers, row-count checks and transaction rollback. They deliberately
// prepare rows directly within the package that owns those rows.
func completionFixture(t *testing.T, role string) (*database.DB, *Run, *schema.Spec) {
	t.Helper()
	return completionSpecFixture(t, schema.Spec{Kind: "pipeline", Role: role, Stages: []schema.Stage{{Name: "finish"}}})
}

func completionSpecFixture(t *testing.T, spec schema.Spec) (*database.DB, *Run, *schema.Spec) {
	t.Helper()
	db := testDB(t)
	project := workflowTestProject(t, db, "completion", "Completion", "")
	change := admissionTestChange(t, db, project, "A change")
	run := insertStoredRun(t, db, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: project, ChangeID: change, WorkflowName: "test", WorkflowVersion: 1, Kind: "pipeline", Status: RunRunning.String(), CurrentStage: "finish", DefinitionJson: json.RawMessage(mustJSON(spec))})
	return db, run, &spec
}

func noCompletionEffects(context.Context, *database.Tx, CompletedRun) (ReleaseReceipt, error) {
	return ReleaseReceipt{}, nil
}

func TestWorkflowChildCompletionCannotFinishNewerCall(t *testing.T) {
	t.Parallel()
	for _, mismatch := range []string{"attempt", "child identity", "parent stage", "parent abandoned"} {
		t.Run(mismatch, func(t *testing.T) {
			t.Parallel()
			store, parent, spec := completionFixture(t, "working")
			step := insertStoredCall(t, store, parent.ID, "finish", "child", 2, nil)
			childID := insertStoredRun(t, store, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: parent.ProjectID, WorkflowName: "child", WorkflowVersion: 2, Kind: "pipeline", Status: RunRunning.String(), CurrentStage: "finish", DefinitionJson: json.RawMessage(mustJSON(spec)), ParentRunID: parent.ID, ParentStepID: step}).ID
			if err := requireRunWrite(workflowsql.New(store.SQL()).SetParentStepAttempt(context.Background(), workflowsql.SetParentStepAttemptParams{ID: childID, ParentStepAttempt: 2})); err != nil {
				t.Fatal(err)
			}
			recordStoredCallEvidence(t, store, step, 2, json.RawMessage(mustJSON(map[string]any{"childRunId": childID})))
			switch mismatch {
			case "attempt":
				if err := requireRunWrite(workflowsql.New(store.SQL()).SetParentStepAttempt(context.Background(), workflowsql.SetParentStepAttemptParams{ID: childID, ParentStepAttempt: 1})); err != nil {
					t.Fatal(err)
				}
			case "child identity":
				recordStoredCallEvidence(t, store, step, 2, json.RawMessage(`{"childRunId":"00000001-869f-7000-8000-000000000000"}`))
			case "parent stage":
				if err := requireRunWrite(workflowsql.New(store.SQL()).TransitionRun(context.Background(), workflowsql.TransitionRunParams{At: time.Now(), ID: parent.ID, ExpectedStatus: parent.Status.String(), CurrentStage: parent.CurrentStage, RetryRevision: parent.RetryRevision, NextStatus: parent.Status.String(), NextStage: "later"})); err != nil {
					t.Fatal(err)
				}
			case "parent abandoned":
				if err := requireRunWrite(workflowsql.New(store.SQL()).TransitionRun(context.Background(), workflowsql.TransitionRunParams{At: time.Now(), ID: parent.ID, ExpectedStatus: parent.Status.String(), CurrentStage: parent.CurrentStage, RetryRevision: parent.RetryRevision, NextStatus: RunAbandoned.String(), NextStage: parent.CurrentStage})); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := AddArtifactIn(context.Background(), store, childID, "preview", "old preview", "", "", time.Now()); err != nil {
				t.Fatal(err)
			}
			child, err := readRun(context.Background(), store, childID)
			if err != nil {
				t.Fatal(err)
			}
			if err := (advanceStore{db: store, completion: completionRecorderFunc(noCompletionEffects)}).complete(context.Background(), child); err != nil {
				t.Fatal(err)
			}
			call := storedStep(t, store, step)
			expect(t, call.Status == StatusRunning && call.Attempt == 2, "newer call was finished: %s attempt %d", call.Status, call.Attempt)
			expect(t, eventCount(t, store, parent.ID, "step.finished") == 0, "parent recorded a finish")
			expect(t, len(seenArtifacts(t, store, parent.ID)) == 0, "stale child artifacts reached the parent")
		})
	}
}

func TestWorkflowTransitionConcurrentWinner(t *testing.T) {
	t.Parallel()
	spec := schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "finish"}, {Name: "next", Steps: []schema.Step{{Name: "accept", Uses: "approval"}}}}}
	q, run := admitWorkflowSpec(t, spec)
	store := q.db
	results := make(chan error, 8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := (advanceStore{db: store, stages: NewStages(nil, func(string) bool { return false })}).transition(context.Background(), run, "next", "alice")
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
		} else if !errors.Is(err, ErrRunMoved) {
			t.Fatal(err)
		}
	}
	if winners != 1 {
		t.Fatalf("%d transition winners", winners)
	}
	expect(t, len(seenSteps(t, store, run.ID)) == 1, "stage steps were admitted more than once")
	expect(t, eventCount(t, store, run.ID, "advance.ok") == 1, "advancement recorded more than once")
}

func TestWorkflowChildCompletionAtomicHandoff(t *testing.T) {
	t.Parallel()
	store, parent, spec := completionFixture(t, "working")
	step := insertStoredStep(t, store, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: parent.ID, Stage: "finish", Name: "child", Type: "workflow/call", Status: StatusRunning.String()})
	childID := insertStoredRun(t, store, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: parent.ProjectID, WorkflowName: "child", WorkflowVersion: 2, Kind: "pipeline", Status: RunRunning.String(), CurrentStage: "finish", DefinitionJson: json.RawMessage(mustJSON(spec)), ParentRunID: parent.ID, ParentStepID: step}).ID
	recordStoredCallEvidence(t, store, step, 0, json.RawMessage(mustJSON(map[string]any{"childRunId": childID})))
	if _, err := AddArtifactIn(context.Background(), store, childID, "preview", "image", "", string([]byte{0, 255, 42}), time.Now()); err != nil {
		t.Fatal(err)
	}
	child, err := readRun(context.Background(), store, childID)
	if err != nil {
		t.Fatal(err)
	}
	// The participant fails after Work has written the child handoff. Both
	// its receipt and Work's changes must roll back together.
	refusal := errors.New("participant refused")
	failing := advanceStore{db: store, completion: completionRecorderFunc(func(ctx context.Context, tx *database.Tx, in CompletedRun) (ReleaseReceipt, error) {
		if err := recordReceiptIn(ctx, tx, in.RunID, "test.participant", struct{}{}, time.Now()); err != nil {
			return ReleaseReceipt{}, err
		}
		return ReleaseReceipt{}, refusal
	})}
	if err := failing.complete(context.Background(), child); !errors.Is(err, refusal) {
		t.Fatalf("completion: %v", err)
	}
	expect(t, storedStep(t, store, step).Status == StatusRunning, "refused handoff finished the call")
	expect(t, len(seenArtifacts(t, store, parent.ID)) == 0, "refused handoff copied artifacts")
	expect(t, storedRun(t, store, child.ID).Status == RunRunning, "refused handoff finished the child")
	if err := (advanceStore{db: store, completion: completionRecorderFunc(noCompletionEffects)}).complete(context.Background(), child); err != nil {
		t.Fatal(err)
	}
	if err := (advanceStore{db: store, completion: completionRecorderFunc(noCompletionEffects)}).complete(context.Background(), child); !errors.Is(err, ErrRunMoved) {
		t.Fatalf("repeat completion: %v", err)
	}
	finished := storedStep(t, store, step)
	expect(t, finished.Status == StatusPass && jsonMatches(t, finished.Evidence, "outcome=done"), "call: %s %s", finished.Status, finished.Evidence)
	expect(t, binaryArtifacts(t, store, parent.ID, []byte{0, 255, 42}) == 1, "child artifact was not handed to the parent")
	wantEventCount(t, store, 0, ofKind("test.participant"))
	expect(t, eventCount(t, store, parent.ID, "step.finished") == 1, "parent finish receipts")
}

func TestWorkflowChildCompletionPreservesExactIdentity(t *testing.T) {
	t.Parallel()
	// Neighbouring ids, one bit apart: the child is matched exactly.
	linked := idstest.Parse(t, "01a12f00-0000-7000-8000-000000000003")
	for _, childID := range []ids.ID{linked, idstest.Parse(t, "01a12f00-0000-7000-8000-000000000002")} {
		t.Run(fmt.Sprint(childID), func(t *testing.T) {
			store, parent, spec := completionFixture(t, "working")
			step := insertStoredStep(t, store, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: parent.ID, Stage: "finish", Name: "child", Type: "workflow/call", Status: StatusRunning.String()})
			recordStoredCallEvidence(t, store, step, 0, json.RawMessage(fmt.Sprintf(`{"childRunId":%q,"identifier":9007199254740993,"startedAtMs":1}`, linked)))
			// sql-guarantee: Force neighbouring identities to test exact child completion matching; InsertRun mints its own id and cannot supply either identity.
			completionExec(t, store, `INSERT INTO runs(id,number,project_id,workflow_name,workflow_version,kind,current_stage,definition_json,parent_run_id,parent_step_id,created_at,updated_at) VALUES ($1,99,$2,'child',1,'pipeline','finish',$3,$4,$5,'2026-10-09T00:00:00Z','2026-10-09T00:00:00Z')`, childID, parent.ProjectID, mustJSON(spec), parent.ID, step)
			child, err := readRun(context.Background(), store, childID)
			if err != nil {
				t.Fatal(err)
			}
			if err := (advanceStore{db: store, completion: completionRecorderFunc(noCompletionEffects)}).complete(context.Background(), child); err != nil {
				t.Fatal(err)
			}
			status := "running"
			if linked == childID {
				status = "pass"
				got := storedStep(t, store, step).Evidence
				duration, numeric := jsonBigint(got, "durationMs")
				expect(t, jsonMatches(t, got, "outcome=done") && numeric && duration > 0 && !hasKey(got, "startedAtMs"), "completed call evidence: %s", got)
			}
			call := storedStep(t, store, step)
			expect(t, call.Status.String() == status && jsonMatches(t, call.Evidence, "identifier=9007199254740993"), "call: %s %s", call.Status, call.Evidence)
		})
	}
}

func TestWorkflowCompletionKeepsExistingLandingIntent(t *testing.T) {
	t.Parallel()
	store, run, _ := completionFixture(t, "working")
	// sql-guarantee: Preserve a historical already-applied intent with no landing run; AttachAcceptedLanding always names one and cannot author this existing receipt.
	completionExec(t, store, `INSERT INTO workflow_landings(work_run_id,acceptance_applied) VALUES ($1,true)`, run.ID)
	// The existing intent is sufficient even if an insert cannot create a row.
	completionExec(t, store, `CREATE FUNCTION veto_landing() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NULL; END $$`)
	completionExec(t, store, `CREATE TRIGGER veto_landing BEFORE INSERT ON workflow_landings FOR EACH ROW EXECUTE FUNCTION veto_landing()`)
	if err := (advanceStore{db: store, completion: completionRecorderFunc(noCompletionEffects)}).complete(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	landing, ok := seenLanding(t, store, run.ID)
	expect(t, ok && landing.AcceptanceApplied && landing.LandingRunID.IsZero(), "existing landing intent: %v %+v", ok, landing)
	expect(t, eventCount(t, store, run.ID, "run.done") == 1, "run.done receipts")
}

func TestWorkflowStoreTransitionCommitsWholeStage(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	projectID := workflowTestProject(t, db, "stage-test", "Stage test", "")
	spec := schema.Spec{Stages: []schema.Stage{
		{Name: "before"},
		{Name: "after", OnEnter: []schema.Step{{Name: "prepare", Uses: "script"}}, Steps: []schema.Step{{Name: "approve", Uses: "approval"}}},
	}}
	run := insertStoredRun(t, db, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: projectID, WorkflowName: "test", WorkflowVersion: 1, Kind: "pipeline", Status: RunRunning.String(), CurrentStage: "before", DefinitionJson: json.RawMessage(mustJSON(spec))})
	runID, store := run.ID, db
	// Fail the second step insert, after the stage update, ledger writes, and
	// first step insert have all happened. None may survive this transaction.
	if _, err := db.SQL().ExecContext(context.Background(), `ALTER TABLE steps ADD CONSTRAINT reject_approval CHECK (name != 'approve')`); err != nil {
		t.Fatal(err)
	}
	if auto, err := (advanceStore{db: store, stages: NewStages(nil, func(kind string) bool { return kind == "script" })}).transition(context.Background(), run, "after", "alice"); err == nil || len(auto) != 0 {
		t.Fatalf("failed transition = %v, %v", auto, err)
	}
	current, err := readRun(context.Background(), store, runID)
	if err != nil {
		t.Fatal(err)
	}
	if current.CurrentStage != "before" {
		t.Fatalf("partial stage transition: %s", current.CurrentStage)
	}
	if steps, events := seenSteps(t, store, runID), seenEvents(t, store, runID); len(steps) != 0 || len(events) != 0 {
		t.Fatalf("partial records after rollback: %d steps, %d events", len(steps), len(events))
	}
	if _, err := db.SQL().ExecContext(context.Background(), `ALTER TABLE steps DROP CONSTRAINT reject_approval`); err != nil {
		t.Fatal(err)
	}
	auto, err := (advanceStore{db: store, stages: NewStages(nil, func(kind string) bool { return kind == "script" })}).transition(context.Background(), run, "after", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(auto) != 1 || auto[0].Name != "prepare" || !auto[0].OnEnter {
		t.Fatalf("dispatch plan = %+v", auto)
	}
	// A separate connection can see the complete stage before the caller is
	// given anything to dispatch.
	current, err = readRun(context.Background(), store, runID)
	if err != nil {
		t.Fatal(err)
	}
	steps, err := readStageSteps(context.Background(), store, runID, "after")
	if err != nil {
		t.Fatal(err)
	}
	if current.CurrentStage != "after" || len(steps) != 2 {
		t.Fatalf("committed stage: %+v, steps %+v", current, steps)
	}
	if count := eventCount(t, store, runID, "advance.ok,stage.entered"); count != 2 {
		t.Fatalf("stage ledger contains %d events", count)
	}
	if count := eventCount(t, store, runID, "advance.ok", "by=alice"); count != 1 {
		t.Fatalf("lost actor: %d advance.ok receipts by alice", count)
	}
}

func TestWorkflowParentFollowUpCannotAdvanceAnotherStage(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"repeat", "new call", "other child", "abandoned", "stale snapshot"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			s, parent, _ := completionSpecFixture(t, schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "finish"}, {Name: "later"}}})
			step := insertStoredStep(t, s, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: parent.ID, Stage: "finish", Name: "child", Type: "workflow/call", Status: StatusRunning.String()})
			childSpec := &schema.Spec{Kind: "pipeline", Stages: []schema.Stage{{Name: "finish"}}}
			childID := insertStoredRun(t, s, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: parent.ProjectID, WorkflowName: "child", WorkflowVersion: 1, Kind: "pipeline", Status: RunRunning.String(), CurrentStage: "finish", DefinitionJson: json.RawMessage(mustJSON(childSpec)), ParentRunID: parent.ID, ParentStepID: step}).ID
			recordStoredCallEvidence(t, s, step, 0, json.RawMessage(mustJSON(map[string]any{"childRunId": childID})))
			child, err := readRun(context.Background(), s, childID)
			if err != nil {
				t.Fatal(err)
			}
			if err := (advanceStore{db: s, completion: completionRecorderFunc(noCompletionEffects)}).complete(context.Background(), child); err != nil {
				t.Fatal(err)
			}
			owner := advanceStore{db: s, stages: NewStages(nil, func(string) bool { return false })}
			switch mode {
			case "new call":
				// sql-guarantee: Change only the attempt of an already-completed call so the old child cannot advance its parent; StartParentCall also clears finished_at and cannot preserve this completed record.
				completionExec(t, s, `UPDATE steps SET attempt=attempt+1 WHERE id=$1`, step)
			case "other child":
				if err := requireAttemptWrite(workflowsql.New(s.SQL()).RecordApproval(context.Background(), workflowsql.RecordApprovalParams{At: time.Now(), ID: step, Attempt: 0, ExpectedStatus: StatusPass.String(), Status: StatusPass.String(), EvidenceJson: json.RawMessage(`{"childRunId":"00000000-03e7-7000-8000-000000000000","outcome":"done"}`)})); err != nil {
					t.Fatal(err)
				}
			case "abandoned":
				if err := requireRunWrite(workflowsql.New(s.SQL()).TransitionRun(context.Background(), workflowsql.TransitionRunParams{At: time.Now(), ID: parent.ID, ExpectedStatus: parent.Status.String(), CurrentStage: parent.CurrentStage, RetryRevision: parent.RetryRevision, NextStatus: RunAbandoned.String(), NextStage: parent.CurrentStage})); err != nil {
					t.Fatal(err)
				}
			case "stale snapshot":
				snapshot, err := readRun(context.Background(), s, parent.ID)
				if err != nil || snapshot == nil {
					t.Fatalf("parent: %+v %v", snapshot, err)
				}
				// sql-guarantee: Isolate retry_revision on the captured Run; InvalidateRunDecision also changes UpdatedAt, which lockRunSnapshotIn compares and could independently reject this stale transition.
				completionExec(t, s, `UPDATE runs SET retry_revision=retry_revision+1 WHERE id=$1`, snapshot.ID)
				_, err = owner.transition(context.Background(), snapshot, "later", "test")
				if !errors.Is(err, ErrRunMoved) {
					t.Fatalf("stale advance: %v", err)
				}
				got := storedRun(t, s, parent.ID)
				expect(t, got.CurrentStage == "finish" && got.Status == RunRunning, "stale snapshot advanced the parent: %+v", got)
				return
			}
			for range 2 {
				current, err := readRun(context.Background(), s, childID)
				if err != nil {
					t.Fatal(err)
				}
				parent, err := owner.completedParent(context.Background(), current)
				if err != nil {
					t.Fatal(err)
				}
				if parent != nil {
					if _, err := owner.transition(context.Background(), parent, "later", "test"); err != nil {
						t.Fatal(err)
					}
				}
			}
			wantStage := "finish"
			if mode == "repeat" {
				wantStage = "later"
			}
			got := storedRun(t, s, parent.ID)
			expect(t, got.CurrentStage == wantStage && got.Status != RunDone, "parent: %+v", got)
		})
	}
}

func TestWorkflowCompletionRequiresAuxiliaryWrites(t *testing.T) {
	t.Parallel()
	for _, record := range []string{"shipping", "landing", "parent result"} {
		t.Run(record, func(t *testing.T) {
			role := "landing"
			if record == "landing" || record == "parent result" {
				role = "working"
			}
			store, run, spec := completionFixture(t, role)
			table, write := "changes", "UPDATE"
			switch record {
			case "landing":
				table, write = "workflow_landings", "INSERT"
			case "parent result":
				step := insertStoredStep(t, store, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "finish", Name: "child", Type: "workflow/call", Status: StatusRunning.String()})
				child := insertStoredRun(t, store, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: run.ProjectID, WorkflowName: "child", WorkflowVersion: 1, Kind: "pipeline", Status: RunRunning.String(), CurrentStage: "finish", DefinitionJson: json.RawMessage(mustJSON(spec)), ParentRunID: run.ID, ParentStepID: step}).ID
				recordStoredCallEvidence(t, store, step, 0, json.RawMessage(mustJSON(map[string]any{"childRunId": child})))
				var err error
				run, err = readRun(context.Background(), store, child)
				if err != nil {
					t.Fatal(err)
				}
				table = "steps"
			}
			completionExec(t, store, `CREATE FUNCTION veto_completion() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NULL; END $$`)
			completionExec(t, store, `CREATE TRIGGER veto_completion BEFORE `+write+` ON `+table+` FOR EACH ROW EXECUTE FUNCTION veto_completion()`)
			before := completionSnapshot(t, store)
			if err := (advanceStore{db: store, completion: completionRecorderFunc(noCompletionEffects)}).complete(context.Background(), run); err == nil {
				t.Fatal("completion succeeded without a required record")
			}
			if after := completionSnapshot(t, store); after != before {
				t.Fatal("failed completion left state or receipts")
			}
			// sql-guarantee: no landing reservation may exist under any run, including one the snapshot predates.
			completionCount(t, store, 0, `SELECT COUNT(*) FROM workflow_landings`)
			completionExec(t, store, `DROP TRIGGER veto_completion ON `+table)
			if err := (advanceStore{db: store, completion: completionRecorderFunc(noCompletionEffects)}).complete(context.Background(), run); err != nil {
				t.Fatalf("explicit advance after failure: %v", err)
			}
		})
	}
}

// Compare all Work rows and receipts, including relationships, across failure.
func completionSnapshot(t *testing.T, db *database.DB) string {
	t.Helper()
	var snapshot string
	// sql-guarantee: an exact image of every Work row and its relationships, compared across a failed completion; the storage representation is the guarantee
	if err := db.SQL().QueryRowContext(context.Background(), `SELECT jsonb_build_array(
 (SELECT jsonb_agg(to_jsonb(r) ORDER BY id) FROM runs r),
 (SELECT jsonb_agg(to_jsonb(s) ORDER BY id) FROM steps s),
 (SELECT jsonb_agg(to_jsonb(e) ORDER BY id) FROM run_events e),
 (SELECT jsonb_agg(to_jsonb(c) ORDER BY id) FROM changes c),
 (SELECT jsonb_agg(to_jsonb(l) ORDER BY work_run_id) FROM workflow_landings l))::text`).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestCompletionRequestsReleaseOnlyForStandingDeploymentInItsTree(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"main", "prod", "default", "preview", "nested", "other project", "no deploy"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			db, run, spec := completionFixture(t, "landing")
			deployed := run.ID
			config := `{"deployment":"main"}`
			switch scenario {
			case "prod":
				config = `{"deployment":"prod"}`
			case "default":
				config = `{}`
			case "preview":
				config = `{"deployment":"preview"}`
			case "nested", "other project":
				project := run.ProjectID
				if scenario == "other project" {
					project = workflowTestProject(t, db, "foreign", "Foreign", "")
				}
				deployed = insertStoredRun(t, db, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: project, WorkflowName: "child", WorkflowVersion: 1, Kind: "pipeline", Status: RunRunning.String(), CurrentStage: "finish", DefinitionJson: json.RawMessage(mustJSON(spec)), ParentRunID: run.ID}).ID
			}
			if scenario != "no deploy" {
				insertStoredStep(t, db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: deployed, Stage: "finish", Name: "deploy", Type: "deploy", Status: StatusPass.String(), ConfigJson: json.RawMessage(config)})
			}
			want := scenario != "preview" && scenario != "no deploy" && scenario != "other project"
			calls := 0
			owner := advanceStore{db: db, completion: completionRecorderFunc(func(_ context.Context, _ *database.Tx, in CompletedRun) (ReleaseReceipt, error) {
				calls++
				if in.Release != want {
					t.Errorf("release requested=%v want=%v", in.Release, want)
				}
				return ReleaseReceipt{ID: idstest.N(7), Number: 8}, nil
			})}
			if err := owner.complete(context.Background(), run); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("participant calls: %d", calls)
			}
			expected := 0
			if want {
				expected = 1
			}
			wantEventCount(t, db, expected, kindWith("release.created", "number", "8"))
		})
	}
}

// A landing's release names who approved it: the people whose votes passed
// its approvals, each once. An overridden approval is no one's yes, and a
// rejection is not an approval.
func TestCompletionReleaseNamesWhoApprovedTheLanding(t *testing.T) {
	t.Parallel()
	db, run, _ := completionFixture(t, "landing")
	insertStoredStep(t, db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "finish", Name: "deploy", Type: "deploy", Status: StatusPass.String(), ConfigJson: json.RawMessage(`{"deployment":"main"}`)})
	// No owner operation records a finished vote without the approval
	// machinery around it; the recorded evidence is the input under test.
	approved := func(name, overrideBy, evidence string) {
		t.Helper()
		step := insertStoredStep(t, db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "finish", Name: name, Type: "approval", Status: StatusPending.String()})
		if n, err := workflowsql.New(db.SQL()).RecordApproval(context.Background(), workflowsql.RecordApprovalParams{At: time.Now(), Status: StatusPass.String(), EvidenceJson: json.RawMessage(evidence), ID: step, Attempt: 0, ExpectedStatus: StatusPending.String()}); err != nil || n != 1 {
			t.Fatalf("record %s: %d %v", name, n, err)
		}
		if overrideBy != "" {
			// sql-guarantee: an overridden approval is a stored fact no test operation produces alone.
			if _, err := db.SQL().ExecContext(context.Background(), `UPDATE steps SET override_by=$1 WHERE id=$2`, overrideBy, step); err != nil {
				t.Fatal(err)
			}
		}
	}
	approved("approve-landing", "", `{"decisions":[{"approver":"carol","decision":"reject","comment":"","at":""},{"approver":"bls","decision":"approve","comment":"","at":""},{"approver":"dana via mcp/claude","decision":"approve","comment":"","at":""}],"quorum":2,"approvals":2}`)
	approved("second-look", "", `{"decisions":[{"approver":"bls","decision":"approve","comment":"","at":""}],"quorum":1,"approvals":1}`)
	approved("waived", "erin", `{"decisions":[{"approver":"frank","decision":"approve","comment":"","at":""}],"quorum":1,"approvals":1}`)
	var got string
	owner := advanceStore{db: db, completion: completionRecorderFunc(func(_ context.Context, _ *database.Tx, in CompletedRun) (ReleaseReceipt, error) {
		got = in.ApprovedBy
		return ReleaseReceipt{ID: idstest.N(7), Number: 8}, nil
	})}
	if err := owner.complete(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if got != "bls, dana via mcp/claude" {
		t.Fatalf("release approved by %q", got)
	}
}
