package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"

	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/dbtest"
)

// These tests are about the handoff's transactions, not about who may
// approve; acceptance_carry_test.go covers eligibility.
func anyoneMayApprove(context.Context, *Run, *Step, access.Actor) error { return nil }

func approvingLandings(db *database.DB, adoptions LandingAdoptions) landings {
	owner := newLandings(db, adoptions)
	owner.eligible = anyoneMayApprove
	return owner
}

const landingMeans = "this merges and ships on my say-so"

// acceptedShowing is a closed acceptance whose vote recorded what the landing
// would ask.
func acceptedShowing(shown ...string) string {
	return mustJSON(map[string]any{"approver": "alice", "decisions": []map[string]any{{"approver": "alice", "principal": idstest.N(21).String(), "via": "session", "decision": "approve", "alsoApproves": shown}}})
}

func landingAcceptanceFixture(t *testing.T) (landings, *Run, *Run, []ids.ID) {
	t.Helper()
	return landingAcceptanceFixtureShowing(t, landingMeans)
}

// landingAcceptanceFixtureShowing is an accepted change whose acceptance
// showed the given landing sentences, and its landing at the first stage.
func landingAcceptanceFixtureShowing(t *testing.T, shown ...string) (landings, *Run, *Run, []ids.ID) {
	t.Helper()
	store, work, _ := completionFixture(t, "working")
	if err := (advanceStore{db: store, completion: completionRecorderFunc(noCompletionEffects)}).complete(context.Background(), work); err != nil {
		t.Fatal(err)
	}
	accepted := insertStoredStep(t, store, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: work.ID, Stage: "finish", Name: "accept", Type: "approval", Status: StatusPending.String()})
	if changed, err := workflowsql.New(store.SQL()).RecordApproval(context.Background(), workflowsql.RecordApprovalParams{At: time.Now(), Status: StatusPass.String(), EvidenceJson: json.RawMessage(acceptedShowing(shown...)), ID: accepted, ExpectedStatus: StatusPending.String()}); err != nil || changed != 1 {
		t.Fatalf("stored acceptance: changed=%d %v", changed, err)
	}
	means := map[string]any{"means": landingMeans}
	def := schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{{Name: "start", Steps: []schema.Step{{Name: "first", Uses: "approval", With: means}, {Name: "second", Uses: "approval", With: means}}}, {Name: "later", Steps: []schema.Step{{Name: "review", Uses: "approval", With: means}}}}}
	landingID := insertStoredRun(t, store, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: work.ProjectID, ChangeID: work.ChangeID, WorkflowName: "land", WorkflowVersion: 1, Kind: "pipeline", CurrentStage: "start", DefinitionJson: json.RawMessage(mustJSON(def)), Status: RunRunning.String()}).ID
	stepIDs := []ids.ID{
		insertStoredStep(t, store, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: landingID, Stage: "start", Name: "first", Type: "approval", ConfigJson: json.RawMessage(mustJSON(means)), Status: StatusPending.String()}),
		insertStoredStep(t, store, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: landingID, Stage: "start", Name: "second", Type: "approval", ConfigJson: json.RawMessage(mustJSON(means)), Status: StatusPending.String()}),
	}
	changed, err := workflowsql.New(store.SQL()).AttachAcceptedLanding(context.Background(), workflowsql.AttachAcceptedLandingParams{LandingRunID: landingID, WorkRunID: work.ID})
	if err != nil || changed != 1 {
		t.Fatalf("attach accepted landing: changed=%d %v", changed, err)
	}
	landing, err := readRun(context.Background(), store, landingID)
	if err != nil {
		t.Fatal(err)
	}
	return landings{db: store, eligible: anyoneMayApprove}, work, landing, stepIDs
}

// storeReceipts counts run receipts of the given kinds anywhere in the store,
// so a receipt written to an unrelated run still counts.
func storeReceipts(t *testing.T, db database.Queryer, kinds string) int {
	t.Helper()
	return countEvents(storedEvents(t, db), func(e Event) bool { return listHas(kinds, e.Kind) })
}

// appliedLandings counts applied landing reservations anywhere in the store.
// Every reservation is keyed by an existing work run, so reading each stored
// run's reservation is complete.
func appliedLandings(t *testing.T, db database.Queryer) int {
	t.Helper()
	n := 0
	for _, run := range storedRuns(t, db) {
		if landing, ok := seenLanding(t, db, run.ID); ok && landing.AcceptanceApplied {
			n++
		}
	}
	return n
}

func acceptanceApplied(t *testing.T, db database.Queryer, work *Run) bool {
	t.Helper()
	landing, ok := seenLanding(t, db, work.ID)
	if !ok {
		t.Fatalf("work run %d has no landing reservation", work.ID)
	}
	return landing.AcceptanceApplied
}

func TestLandingAcceptanceRollsBackMarkerAndReceipts(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"marker", "missing marker", "receipt", "second receipt", "commit"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			store, work, landing, _ := landingAcceptanceFixture(t)
			switch failure {
			case "marker":
				dbtest.FailNextStatement(t, store.db, "UPDATE workflow_landings SET acceptance_applied")
			case "receipt":
				completionExec(t, store.db, `ALTER TABLE run_events ADD CONSTRAINT reject_landing CHECK(kind<>'landing.autostarted')`)
			case "second receipt":
				completionExec(t, store.db, fmt.Sprintf(`ALTER TABLE run_events ADD CONSTRAINT reject_work_receipt CHECK(kind<>'landing.autostarted' OR run_id<>'%s')`, work.ID))
			default:
				body, trigger := "RETURN NULL;", "CREATE TRIGGER veto_acceptance BEFORE UPDATE ON workflow_landings"
				if failure == "commit" {
					body = "RAISE EXCEPTION 'deferred acceptance failure';"
					trigger = "CREATE CONSTRAINT TRIGGER veto_acceptance AFTER UPDATE ON workflow_landings DEFERRABLE INITIALLY DEFERRED"
				}
				completionExec(t, store.db, `CREATE FUNCTION veto_acceptance() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN `+body+` END $$`)
				completionExec(t, store.db, trigger+` FOR EACH ROW EXECUTE FUNCTION veto_acceptance()`)
			}
			if result, err := store.apply(context.Background(), work.ID, landing.ID); err == nil || result != nil {
				t.Fatalf("failed handoff: %+v %v", result, err)
			}
			expect(t, stepCount(seenSteps(t, store.db, landing.ID), func(s *Step) bool { return s.Status == StatusPending && emptyJSON(s.Evidence) }) == 2, "failed handoff moved landing gates")
			expect(t, !acceptanceApplied(t, store.db, work), "failed handoff kept its marker")
			expect(t, storeReceipts(t, store.db, "approval,landing.autostarted") == 0, "failed handoff kept receipts")
		})
	}
}
func TestLandingAcceptanceConcurrentRetriesWriteOneHandoff(t *testing.T) {
	t.Parallel()
	store, work, landing, _ := landingAcceptanceFixture(t)
	// Concurrent retries may return the same committed handoff, but may not
	// duplicate its marker or receipts. The handoff casts no votes: the
	// acceptance is judged by carry, as the stage's steps answer.
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := store.apply(context.Background(), work.ID, landing.ID)
			if err != nil || result == nil || result.ID != landing.ID {
				t.Errorf("handoff: %+v %v", result, err)
			}
		}()
	}
	wg.Wait()
	expect(t, acceptanceApplied(t, store.db, work) && appliedLandings(t, store.db) == 1, "handoff marker missing or duplicated")
	expect(t, storeReceipts(t, store.db, "approval") == 0, "handoff cast votes")
	expect(t, storeReceipts(t, store.db, "landing.autostarted") == 2, "handoff receipts duplicated or missing")
}
func TestLandingAcceptanceCannotCrossItsScope(t *testing.T) {
	t.Parallel()
	for _, mutation := range []string{"wrong landing", "wrong project", "wrong change", "wrong owner", "active work", "child work", "wrong role"} {
		t.Run(mutation, func(t *testing.T) {
			t.Parallel()
			store, work, landing, _ := landingAcceptanceFixture(t)
			switch mutation {
			case "wrong landing":
				// The landing record points at a different landing run than the one accepted.
				other := insertStoredRun(t, store.db, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: landing.ProjectID, ChangeID: landing.ChangeID, WorkflowName: landing.WorkflowName, WorkflowVersion: int64(landing.WorkflowVersion), Kind: landing.Kind, CurrentStage: landing.CurrentStage, DefinitionJson: json.RawMessage(landing.DefinitionJSON), Status: RunRunning.String()}).ID
				// sql-guarantee: the scope law changes only the coordinator landing identity; AttachAcceptedLanding is called only at the landing's creation.
				completionExec(t, store.db, `UPDATE workflow_landings SET landing_run_id=? WHERE work_run_id=?`, other, work.ID)
			case "wrong project":
				pid := workflowTestProject(t, store.db, "other", "Other", "")
				// sql-guarantee: the landing scope law requires deliberately disagreeing stored project identities; owner mutations forbid project reassignment.
				completionExec(t, store.db, `UPDATE runs SET project_id=?,number=number+100000 WHERE id=?`, pid, landing.ID)
			case "wrong change":
				ch := admissionTestChange(t, store.db, work.ProjectID, "Other")
				// sql-guarantee: the landing scope law requires a deliberately disagreeing stored Change identity; no Workflow mutation reassigns a run relationship.
				completionExec(t, store.db, `UPDATE runs SET change_id=? WHERE id=?`, ch, landing.ID)
			case "wrong owner":
				pid := workflowTestProject(t, store.db, "other", "Other", "")
				// sql-guarantee: the landing scope law requires deliberately disagreeing stored project identities; owner mutations forbid project reassignment.
				completionExec(t, store.db, `UPDATE changes SET project_id=?,number=number+100000 WHERE id=?`, pid, work.ChangeID)
			case "active work":
				transitionStoredRunForTest(t, store.db, work.ID, "", RunRunning)
			case "child work":
				// sql-guarantee: the landing scope matrix changes only immutable parent identity; no generated mutation changes parent_run_id.
				completionExec(t, store.db, `UPDATE runs SET parent_run_id=? WHERE id=?`, landing.ID, work.ID)
			case "wrong role":
				// sql-guarantee: the landing scope matrix changes only immutable role declaration; no generated mutation changes definition_json.
				completionExec(t, store.db, `UPDATE runs SET definition_json='{"kind":"pipeline","role":"working","stages":[{"name":"start"}]}' WHERE id=?`, landing.ID)
			}
			if result, err := store.apply(context.Background(), work.ID, landing.ID); err == nil || result != nil {
				t.Fatalf("scope admitted: %+v %v", result, err)
			}
			expect(t, storeReceipts(t, store.db, "approval,landing.autostarted") == 0, "out-of-scope handoff wrote receipts")
			expect(t, appliedLandings(t, store.db) == 0, "out-of-scope handoff set a marker")
		})
	}
}
func TestLandingAcceptanceRetriesOnlyItsOriginalStageAndRevision(t *testing.T) {
	t.Parallel()
	for _, movement := range []string{"stage", "revision", "terminal"} {
		t.Run(movement, func(t *testing.T) {
			t.Parallel()
			store, work, landing, _ := landingAcceptanceFixture(t)
			if _, err := store.apply(context.Background(), work.ID, landing.ID); err != nil {
				t.Fatal(err)
			}
			switch movement {
			case "stage":
				transitionStoredRunForTest(t, store.db, landing.ID, "later", RunRunning)
			case "revision":
				invalidateStoredRunForTest(t, store.db, landing.ID)
			case "terminal":
				transitionStoredRunForTest(t, store.db, landing.ID, "", RunDone)
			}
			result, err := store.apply(context.Background(), work.ID, landing.ID)
			if err != nil || result != nil {
				t.Fatalf("retry crossed handoff: %+v %v", result, err)
			}
			expect(t, storeReceipts(t, store.db, "approval") == 0, "retry cast votes")
		})
	}
}
func TestLandingAcceptancePreservesQuorumAndReportsMalformedEvidence(t *testing.T) {
	t.Parallel()
	for _, corrupt := range []bool{false, true} {
		t.Run(fmt.Sprint(corrupt), func(t *testing.T) {
			t.Parallel()
			store, work, landing, stepIDs := landingAcceptanceFixture(t)
			changed, err := workflowsql.New(store.db.SQL()).RetryStep(context.Background(), workflowsql.RetryStepParams{ConfigJson: []byte(`{"quorum":2,"means":"` + landingMeans + `","extra":9007199254740993}`), ID: stepIDs[0], Attempt: 0})
			if err != nil || changed != 1 {
				t.Fatalf("store quorum: changed=%d %v", changed, err)
			}
			if corrupt {
				steps, err := readSteps(context.Background(), store.db, work.ID)
				if err != nil {
					t.Fatal(err)
				}
				for _, step := range steps {
					changed, err := workflowsql.New(store.db.SQL()).RecordApproval(context.Background(), workflowsql.RecordApprovalParams{At: time.Now(), Status: step.Status.String(), EvidenceJson: []byte(`{"decisions":"bad"}`), ID: step.ID, Attempt: step.Attempt, ExpectedStatus: step.Status.String()})
					if err != nil || changed != 1 {
						t.Fatalf("store malformed evidence: changed=%d %v", changed, err)
					}
				}
			}
			result, err := store.apply(context.Background(), work.ID, landing.ID)
			if corrupt {
				if err == nil || result != nil {
					t.Fatalf("bad evidence granted handoff: %+v %v", result, err)
				}
				return
			}
			if err != nil || result == nil {
				t.Fatalf("quorum handoff: %+v %v", result, err)
			}
			if _, err := store.carry(context.Background(), result); err != nil {
				t.Fatal(err)
			}
			gate := storedStep(t, store.db, stepIDs[0])
			expect(t, gate.Status == StatusPending && jsonMatches(t, gate.Evidence, "approvals=1", "quorum=2"), "quorum gate: %s %s", gate.Status, gate.Evidence)
		})
	}
}
func TestLandingAcceptanceCancellationReleasesItsProjectLockWait(t *testing.T) {
	t.Parallel()
	store, work, landing, _ := landingAcceptanceFixture(t)
	tx, err := store.db.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	holdProjectLock(t, tx, work.ProjectID)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := store.apply(ctx, work.ID, landing.ID); done <- err }()
	dbtest.WaitForLock(t, store.db, "-- name: ProjectForUpdate")
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled acceptance kept waiting")
	}
	expect(t, storeReceipts(t, store.db, "landing.autostarted") == 0, "cancelled handoff wrote a receipt")
}

func TestLandingAcceptanceRechecksSupersessionAfterWaiting(t *testing.T) {
	t.Parallel()
	store, work, landing, _ := landingAcceptanceFixture(t)
	replacement := insertStoredRun(t, store.db, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: work.ProjectID, ChangeID: work.ChangeID, WorkflowName: "revise", WorkflowVersion: 1, Kind: "pipeline", CurrentStage: "work", DefinitionJson: []byte(`{}`), Status: RunRunning.String()}).ID
	tx, err := store.db.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	holdProjectLock(t, tx, work.ProjectID)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		result, err := store.apply(ctx, work.ID, landing.ID)
		if result != nil {
			err = fmt.Errorf("superseded Work granted advancement: run %d", result.ID)
		}
		done <- err
	}()
	dbtest.WaitForLock(t, store.db, "-- name: ProjectForUpdate")
	if changed, err := workflowsql.New(tx.SQL()).ContinuationLink(context.Background(), workflowsql.ContinuationLinkParams{SourceRunID: work.ID, RunID: replacement}); err != nil || changed != 1 {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	expect(t, storeReceipts(t, store.db, "approval,landing.autostarted") == 0, "superseded handoff wrote receipts")
	expect(t, appliedLandings(t, store.db) == 0, "superseded handoff set a marker")
}
func TestLandingAcceptanceCannotAdvanceAStageItDidNotAccept(t *testing.T) {
	t.Parallel()
	store, work, landing, stepIDs := landingAcceptanceFixture(t)
	// The initial gates were independently decided before this handoff arrived.
	for _, id := range stepIDs {
		transitionStoredStepForTest(t, store.db, id, StatusPass)
	}
	transitionStoredRunForTest(t, store.db, landing.ID, "later", RunRunning)
	for range 2 {
		result, err := store.apply(context.Background(), work.ID, landing.ID)
		if err != nil || result != nil {
			t.Fatalf("handoff advanced a later stage: %+v %v", result, err)
		}
	}
	expect(t, acceptanceApplied(t, store.db, work) && appliedLandings(t, store.db) == 1, "handoff marker missing or duplicated")
	expect(t, storeReceipts(t, store.db, "approval") == 0, "handoff cast votes on a later stage")
}
func TestLandingAcceptanceRetryRequiresItsCommittedScopeReceipt(t *testing.T) {
	t.Parallel()
	for _, mutation := range []string{"missing", "malformed", "missing stage"} {
		t.Run(mutation, func(t *testing.T) {
			t.Parallel()
			store, work, landing, _ := landingAcceptanceFixture(t)
			if _, err := store.apply(context.Background(), work.ID, landing.ID); err != nil {
				t.Fatal(err)
			}
			switch mutation {
			case "missing":
				// sql-guarantee: retry recovery must prove the committed landing receipt is mandatory; no owner command deletes a durable receipt.
				completionExec(t, store.db, `DELETE FROM run_events WHERE run_id=? AND kind='landing.autostarted'`, landing.ID)
			case "malformed":
				// sql-guarantee: retry recovery requires a receipt with a deliberately wrong or missing stage field; InsertRunEvent only appends a new receipt and cannot replace the committed one.
				completionExec(t, store.db, `UPDATE run_events SET payload_json=jsonb_set(payload_json,'{stage}','17') WHERE run_id=? AND kind='landing.autostarted'`, landing.ID)
			case "missing stage":
				// sql-guarantee: retry recovery requires a receipt with a deliberately wrong or missing stage field; InsertRunEvent only appends a new receipt and cannot replace the committed one.
				completionExec(t, store.db, `UPDATE run_events SET payload_json=payload_json-'stage' WHERE run_id=? AND kind='landing.autostarted'`, landing.ID)
			}
			if result, err := store.apply(context.Background(), work.ID, landing.ID); err == nil || result != nil {
				t.Fatalf("unproven retry advanced: %+v %v", result, err)
			}
		})
	}
}

func TestWorkflowLandingReusesRunAndPinsAcceptanceScope(t *testing.T) {
	t.Parallel()
	store, work, _ := completionFixture(t, "working")
	if err := (advanceStore{db: store, completion: completionRecorderFunc(noCompletionEffects)}).complete(context.Background(), work); err != nil {
		t.Fatal(err)
	}
	acceptedStep := insertStoredStep(t, store, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: work.ID, Stage: "finish", Name: "accept", Type: "approval", Status: StatusPending.String()})
	if changed, err := workflowsql.New(store.SQL()).RecordApproval(context.Background(), workflowsql.RecordApprovalParams{At: time.Now(), Status: StatusPass.String(), EvidenceJson: json.RawMessage(acceptedShowing(landingMeans)), ID: acceptedStep, ExpectedStatus: StatusPending.String()}); err != nil || changed != 1 {
		t.Fatalf("stored acceptance: changed=%d %v", changed, err)
	}
	stages := NewStages(nil, func(string) bool { return false })
	doc := &schema.Doc{Spec: schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{
		{Name: "start", Steps: []schema.Step{{Name: "accept", Uses: "approval", With: map[string]any{"means": landingMeans}}}},
		{Name: "later", Steps: []schema.Step{{Name: "separate-review", Uses: "approval", With: map[string]any{"means": landingMeans}}}},
	}}}
	create := func() (*Run, error) {
		return (runStartStore{db: store, stages: stages}).create(context.Background(), StartRequest{ProjectID: work.ProjectID, ChangeID: work.ChangeID, WorkflowName: "land", AcceptedRunID: work.ID, InputsJSON: "{}"}, PreparedRun{Workflow: DefinitionRef{Name: "land", Version: 1}, Spec: doc.Spec})
	}
	dbtest.FailNextStatement(t, store, "UPDATE workflow_landings SET landing_run_id")
	if _, err := create(); !errors.Is(err, database.ErrInjectedFailure) {
		t.Fatalf("creation error: %v", err)
	}
	// sql-guarantee: the failed creation leaves no landing run anywhere.
	completionCount(t, store, 1, `SELECT COUNT(*) FROM runs`)
	expect(t, seenChange(t, store, work.ChangeID).State == "in_progress", "failed landing moved the change")
	landing, err := create()
	if err != nil {
		t.Fatal(err)
	}
	again, err := create()
	if err != nil || again.ID != landing.ID {
		t.Fatalf("reused landing: %+v %v", again, err)
	}
	// A failed marker rolls back the handoff before advancement.
	dbtest.FailNextStatement(t, store, "UPDATE workflow_landings SET acceptance_applied")
	if _, err := (landings{db: store, eligible: anyoneMayApprove}).apply(context.Background(), work.ID, landing.ID); !errors.Is(err, database.ErrInjectedFailure) {
		t.Fatalf("acceptance error: %v", err)
	}
	expect(t, stepCount(seenSteps(t, store, landing.ID), func(s *Step) bool { return s.Stage == "start" && s.Status == StatusPending && emptyJSON(s.Evidence) }) == 1, "failed marker moved the start gate")
	expect(t, storedRun(t, store, landing.ID).CurrentStage == "start", "failed marker advanced the landing")
	expect(t, storeReceipts(t, store, "landing.autostarted") == 0, "failed marker kept a receipt")

	accepted, err := (landings{db: store, eligible: anyoneMayApprove}).apply(context.Background(), work.ID, landing.ID)
	if err != nil || accepted == nil {
		t.Fatalf("handoff: %+v %v", accepted, err)
	}
	if _, err := (landings{db: store, eligible: anyoneMayApprove}).carry(context.Background(), accepted); err != nil {
		t.Fatal(err)
	}
	if _, err := (advanceStore{db: store, stages: stages}).transition(context.Background(), accepted, "later", "system"); err != nil {
		t.Fatal(err)
	}
	if again, err := (landings{db: store, eligible: anyoneMayApprove}).apply(context.Background(), work.ID, landing.ID); err != nil || again != nil {
		t.Fatalf("repeat handoff: %+v %v", again, err)
	}
	landingSteps := seenSteps(t, store, landing.ID)
	expect(t, stepCount(landingSteps, func(s *Step) bool { return s.Stage == "later" && s.Status == StatusPending }) == 1, "later stage gate was not left pending")
	var start []*Step
	for _, s := range landingSteps {
		if s.Stage == "start" {
			start = append(start, s)
		}
	}
	if len(start) != 1 {
		t.Fatalf("start stage steps: %d", len(start))
	}
	var approval ApprovalEvidence
	if err := json.Unmarshal(start[0].Evidence, &approval); err != nil {
		t.Fatal(err)
	}
	decisions := approval.Decisions
	if len(decisions) != 1 || decisions[0].Approver != "alice" {
		t.Fatalf("carried votes: %+v", decisions)
	}
	// sql-guarantee: reuse creates no second landing run anywhere.
	completionCount(t, store, 2, `SELECT COUNT(*) FROM runs`)
	expect(t, storeReceipts(t, store, "landing.autostarted") == 2, "handoff receipts")
}
