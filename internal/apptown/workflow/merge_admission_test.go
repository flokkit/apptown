package workflow

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestAdmittedMergeDetachesCallerAndRechecksBeforeEffect(t *testing.T) {
	t.Parallel()
	db, run, _ := completionFixture(t, "working")
	landingAdmittedForTest(t, db, run.ID)
	q := queries{db: db}
	id := insertStoredStep(t, db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "finish", Name: "accept", Type: "scm/merged", Status: StatusPending.String(), ConfigJson: []byte(`{"value":1}`)})
	{
		current, err := workflowsql.New(q.db.SQL()).StepByID(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if rows, err := workflowsql.New(q.db.SQL()).RecordApproval(context.Background(), workflowsql.RecordApprovalParams{At: time.Now(), ID: id, Attempt: current.Attempt, ExpectedStatus: current.Status, Status: StatusPending.String(), EvidenceJson: []byte(`{"value":2}`)}); err != nil || rows != 1 {
			t.Fatalf("stored step state: rows=%d err=%v", rows, err)
		}
	}
	ctx := context.Background()
	step, err := readStep(ctx, q.db, id)
	if err != nil {
		t.Fatal(err)
	}
	source, err := readMergeSourceForRun(ctx, q.db, run)
	if err != nil {
		t.Fatal(err)
	}
	call := MergeCall{Purpose: MergeForAssertion, Run: *run, Step: *step, Source: source}
	permission, err := NewMergeAdmission(q.db, policyLawRules{}).Prepare(ctx, call)
	if err != nil || permission.Admitted == nil {
		t.Fatalf("admission %+v %v", permission, err)
	}
	// Mutating the caller after admission must not alter its accepted snapshot.
	call.Step.Config[0] = '!'
	call.Step.Evidence[0] = '!'
	call.Source.Branch = "other"
	scoped, err := permission.Admitted.Context(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	owner := NewSCMWork(q.db, t.TempDir(), nil)
	claim, err := owner.Prepare(scoped, scm.NewEffect, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if claim.Source().Branch != source.Branch {
		t.Fatal("caller changed accepted branch")
	}
	_, err = database.Transact(ctx, q.db, func(tx *database.Tx) (struct{}, error) {
		refusal, err := claim.CheckIn(ctx, tx, scm.NewEffect)
		if refusal.Reason != "" {
			t.Fatalf("refused unchanged scope: %+v", refusal)
		}
		return struct{}{}, err
	})
	if err != nil {
		t.Fatalf("caller altered accepted step: %v", err)
	}
	{
		current, err := workflowsql.New(q.db.SQL()).StepByID(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if err := requireAttemptWrite(workflowsql.New(q.db.SQL()).StartParentCall(context.Background(), workflowsql.StartParentCallParams{ID: id, Attempt: current.Attempt, OldStatus: current.Status, Status: current.Status, StartedAt: current.StartedAt, EvidenceJson: current.EvidenceJson})); err != nil {
			t.Fatal(err)
		}
	}
	_, err = database.Transact(ctx, q.db, func(tx *database.Tx) (scm.Refusal, error) { return claim.CheckIn(ctx, tx, scm.NewEffect) })
	if !errors.Is(err, errMergeCaller) {
		t.Fatalf("stale effect admitted: %v", err)
	}
	if _, err = (&AdmittedMerge{}).Context(ctx, ""); !errors.Is(err, errMergeCaller) {
		t.Fatalf("empty claim minted: %v", err)
	}
}

func TestSCMWorkReceiptAndChangeSourceAreAtomic(t *testing.T) {
	t.Parallel()
	q, run, _ := activityFixture(t)
	ctx := context.Background()
	source, err := readMergeSourceForRun(ctx, q.db, run)
	if err != nil {
		t.Fatal(err)
	}
	owner := NewSCMWork(q.db, t.TempDir(), nil)
	claim, err := owner.claim(*run, source, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	receipt := scm.Receipt{Kind: scm.Completed, Operation: "test-merge", Branch: source.Branch, SHA: "new-source"}
	completionExec(t, q.db, `CREATE FUNCTION veto_merge_receipt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NULL; END $$`)
	completionExec(t, q.db, `CREATE TRIGGER veto_merge_receipt BEFORE INSERT ON run_events FOR EACH ROW EXECUTE FUNCTION veto_merge_receipt()`)
	_, err = database.Transact(ctx, q.db, func(tx *database.Tx) (struct{}, error) { return struct{}{}, claim.RecordIn(ctx, tx, receipt) })
	if err == nil {
		t.Fatal("missing receipt returned success")
	}
	// sql-guarantee: proves no Change anywhere took the new source before admission; no workflow read enumerates Changes across projects.
	completionCount(t, q.db, 0, `SELECT COUNT(*) FROM changes WHERE landed_sha='new-source'`)
	wantEventCount(t, q.db, 0, anyEvent)
	completionExec(t, q.db, `DROP TRIGGER veto_merge_receipt ON run_events`)
	_, err = database.Transact(ctx, q.db, func(tx *database.Tx) (struct{}, error) { return struct{}{}, claim.RecordIn(ctx, tx, receipt) })
	if err != nil {
		t.Fatal(err)
	}
	wantEqual(t, "source", storedChange(t, q.db, run.ChangeID).LandedSHA, "new-source")
	wantRunEventCount(t, q.db, run.ID, 1, ofKind("merge.completed"))
}

func TestMergeAdmissionCancellationReleasesProjectLockWait(t *testing.T) {
	t.Parallel()
	q, run := mergeGateFixture(t)
	source, err := readMergeSourceForRun(context.Background(), q.db, run)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := q.db.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback()
	holdProjectLock(t, lock, run.ProjectID)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := NewMergeAdmission(q.db, nil).Prepare(ctx, MergeCall{Purpose: MergeForAdvance, Run: *run, Source: source})
		done <- err
	}()
	dbtest.WaitForLock(t, q.db, "-- name: ProjectForUpdate")
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled admission: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled admission retained the database wait")
	}
}

func TestMergeAdmissionRejectsForeignOrWrongAssertionStep(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"foreign step", "wrong step type"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			q, run, id := activityFixture(t)
			if scenario == "foreign step" {
				other := insertStoredRun(t, q.db, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: run.ProjectID, WorkflowName: "other", WorkflowVersion: 1, Kind: "pipeline", Status: RunRunning.String(), CurrentStage: "finish", DefinitionJson: []byte(run.DefinitionJSON)}).ID
				id = insertStoredStep(t, q.db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: other, Stage: "finish", Name: "accept", Type: "scm/merged", Status: StatusPending.String()})
			}
			step, err := readStep(context.Background(), q.db, id)
			if err != nil {
				t.Fatal(err)
			}
			source, err := readMergeSourceForRun(context.Background(), q.db, run)
			if err != nil {
				t.Fatal(err)
			}
			call := MergeCall{Purpose: MergeForAssertion, Run: *run, Step: *step, Source: source}
			permission, err := NewMergeAdmission(q.db, nil).Prepare(context.Background(), call)
			if permission.Admitted != nil || !errors.Is(err, errMergeCaller) {
				t.Fatalf("%s admitted: %+v %v", scenario, permission, err)
			}
			wantEventCount(t, q.db, 0, anyEvent)
		})
	}
}

// A merge into a copy's main is an effect the eval's one rule answers, at
// the merge itself, whichever door led there (an advance or an
// assertion): an eval that may not proceed admits no merge, with why.
func TestAMergeIsAdmittedOnlyWhileItsEvalMayProceed(t *testing.T) {
	t.Parallel()
	for _, why := range []string{"", "eval 3's budget of $5.00 is spent"} {
		db, run, _ := completionFixture(t, "working")
		landingAdmittedForTest(t, db, run.ID)
		evalCopyForTest(t, db, run.ProjectID, idstest.N(3))
		q := queries{db: db}
		id := insertStoredStep(t, db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "finish", Name: "accept", Type: "scm/merged", Status: StatusPending.String(), ConfigJson: []byte(`{}`)})
		ctx := context.Background()
		step, err := readStep(ctx, q.db, id)
		if err != nil {
			t.Fatal(err)
		}
		source, err := readMergeSourceForRun(ctx, q.db, run)
		if err != nil {
			t.Fatal(err)
		}
		permission, err := NewMergeAdmission(q.db, policyLawRules{}).WithEvalGate(refusingEvalGate{why}).Prepare(ctx, MergeCall{Purpose: MergeForAssertion, Run: *run, Step: *step, Source: source})
		if err != nil {
			t.Fatal(err)
		}
		if admitted := permission.Admitted != nil; admitted != (why == "") || (why != "" && permission.Reason != why) {
			t.Fatalf("gate %q: admitted %v, reason %q", why, admitted, permission.Reason)
		}
	}
}

// flippingEvalGate allows until told otherwise, counting the reads.
type flippingEvalGate struct {
	why   *string
	reads *int
}

func (g flippingEvalGate) RefusalIn(context.Context, database.Queryer, ids.ID) (string, error) {
	*g.reads++
	return *g.why, nil
}

// The eval's rule holds at the SCM claim that performs the merge, not only
// at its admission: a rule that changes to "no" after admission refuses
// the effect at the claim's own pre-effect check.
func TestTheEvalGateHoldsAtTheClaimThatMerges(t *testing.T) {
	t.Parallel()
	db, run, _ := completionFixture(t, "working")
	landingAdmittedForTest(t, db, run.ID)
	evalCopyForTest(t, db, run.ProjectID, idstest.N(3))
	q := queries{db: db}
	id := insertStoredStep(t, db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "finish", Name: "accept", Type: "scm/merged", Status: StatusPending.String(), ConfigJson: []byte(`{}`)})
	ctx := context.Background()
	step, err := readStep(ctx, q.db, id)
	if err != nil {
		t.Fatal(err)
	}
	source, err := readMergeSourceForRun(ctx, q.db, run)
	if err != nil {
		t.Fatal(err)
	}
	why, reads := "", 0
	gate := flippingEvalGate{&why, &reads}
	permission, err := NewMergeAdmission(q.db, policyLawRules{}).WithEvalGate(gate).Prepare(ctx, MergeCall{Purpose: MergeForAssertion, Run: *run, Step: *step, Source: source})
	if err != nil || permission.Admitted == nil {
		t.Fatalf("admission: %+v %v", permission, err)
	}
	why = "eval 3's budget is spent since"
	scoped, err := permission.Admitted.Context(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	claim, err := NewSCMWork(q.db, t.TempDir(), nil).Prepare(scoped, scm.NewEffect, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	refusal, err := database.Transact(ctx, q.db, func(tx *database.Tx) (scm.Refusal, error) { return claim.CheckIn(ctx, tx, scm.NewEffect) })
	if err != nil || refusal.Reason != why || reads != 2 {
		t.Fatalf("the claim's check: %+v %v, %d reads", refusal, err, reads)
	}
}
