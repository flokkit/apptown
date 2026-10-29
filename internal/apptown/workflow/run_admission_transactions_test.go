package workflow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
)

// Admission persistence rules belong beside the private writer. SQL here sets
// up exact race and rollback conditions; AppTown exercises the composed commands.
func TestRunStartStoreRollsBackRelationshipsAndStage(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"INSERT INTO run_starts", "UPDATE changes SET state=", "INSERT INTO run_events", "INSERT INTO steps"} {
		t.Run(failure, func(t *testing.T) {
			admissions, req, def := runAdmissionFixture(t)
			store := admissions.records
			dbtest.FailNextStatement(t, store.db, failure)
			if _, err := store.create(context.Background(), req, def); err == nil {
				t.Fatal("injected failure was hidden")
			}
			for _, table := range []string{"runs", "run_starts", "run_events", "steps"} {
				// sql-guarantee: rollback must leave no admission row of any kind anywhere.
				completionCount(t, store.db, 0, `SELECT COUNT(*) FROM `+table)
			}
			ch := seenChange(t, store.db, req.ChangeID)
			expect(t, ch.RunID.IsZero() && ch.State == "in_progress", "change after rollback: run %d %s", ch.RunID, ch.State)
			run, err := store.create(context.Background(), req, def)
			if err != nil {
				t.Fatal(err)
			}
			expect(t, seenChange(t, store.db, req.ChangeID).RunID == run.ID, "change does not name the admitted run")
		})
	}
}

func TestRunStartStoreConcurrentOrdinaryStarts(t *testing.T) {
	t.Parallel()
	admissions, req, def := runAdmissionFixture(t)
	store := admissions.records
	results := race(8, func(int) int {
		_, err := store.create(context.Background(), req, def)
		if err == nil {
			return 0
		}
		if !strings.Contains(err.Error(), "already has a run in flight") {
			t.Errorf("unexpected start failure: %v", err)
		}
		return 1
	})
	if count(results, 0) != 1 || count(results, 1) != 7 {
		t.Fatalf("start winners: %v", results)
	}
	// sql-guarantee: exactly one run exists anywhere; the reads below are then complete for its rows.
	completionCount(t, store.db, 1, `SELECT COUNT(*) FROM runs`)
	winner := seenChange(t, store.db, req.ChangeID).RunID
	start, ok := seenRunStart(t, store.db, winner)
	expect(t, ok && !start.Ready, "winner start: %v %+v", ok, start)
	expect(t, eventCount(t, store.db, winner, "run.created") == 1, "run.created receipts")
	expect(t, len(seenSteps(t, store.db, winner)) == 1, "admitted steps")
}

func TestRunStartStoreChildPinCommitsBeforeExecution(t *testing.T) {
	t.Parallel()
	admissions, req, def := runAdmissionFixture(t)
	store := admissions.records
	def.Spec.Stages[0].Steps = []schema.Step{{Name: "call", Uses: "workflow/call"}}
	parent, err := store.create(context.Background(), req, def)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.prepared(context.Background(), parent.ID); err != nil {
		t.Fatal(err)
	}
	step := admittedStepID(t, queries{db: store.db}, parent, "call")
	req.ParentRunID = parent.ID
	req.ParentStepID = step
	dbtest.FailNextStatement(t, store.db, "UPDATE steps SET status=")
	if _, err := store.create(context.Background(), req, def); err == nil {
		t.Fatal("child pin failure was hidden")
	}
	// sql-guarantee: the failed pin leaves no child run anywhere.
	completionCount(t, store.db, 1, `SELECT COUNT(*) FROM runs`)
	call := storedStep(t, store.db, step)
	expect(t, call.Status == StatusPending && emptyJSON(call.Evidence), "call after failed pin: %s %s", call.Status, call.Evidence)
	runIDs := make([]ids.ID, 8)
	results := race(8, func(i int) int {
		run, err := store.create(context.Background(), req, def)
		if err != nil {
			t.Error(err)
			return 1
		}
		runIDs[i] = run.ID
		return 0
	})
	if count(results, 0) != 8 {
		t.Fatal(results)
	}
	for _, id := range runIDs {
		if id != runIDs[0] {
			t.Fatalf("duplicate children: %v", runIDs)
		}
	}
	// sql-guarantee: concurrent calls create exactly one child anywhere.
	completionCount(t, store.db, 2, `SELECT COUNT(*) FROM runs`)
	call = storedStep(t, store.db, step)
	expect(t, call.Status == StatusRunning && jsonMatches(t, call.Evidence, fmt.Sprintf("childRunId=%s", runIDs[0])), "pinned call: %s %s", call.Status, call.Evidence)
	// The pin names the child by its number too, for people reading the call.
	if child, err := readRun(context.Background(), store.db, runIDs[0]); err != nil || child.Number <= 0 || !jsonMatches(t, call.Evidence, fmt.Sprintf("childRunNumber=%d", child.Number)) {
		t.Fatalf("pinned call does not name the child's number: %s %+v %v", call.Evidence, child, err)
	}
	expect(t, eventCount(t, store.db, parent.ID, "run.called") == 1, "run.called receipts")
	expect(t, seenChange(t, store.db, req.ChangeID).RunID == parent.ID, "child replaced the change's run")
	// Completed children are still the same invocation, without reopening the call.
	{
		current, err := workflowsql.New(store.db.SQL()).RunByID(context.Background(), workflowsql.RunByIDParams{ID: runIDs[0]})
		if err != nil {
			t.Fatal(err)
		}
		if rows, err := workflowsql.New(store.db.SQL()).TransitionRun(context.Background(), workflowsql.TransitionRunParams{At: time.Now(), ID: runIDs[0], ExpectedStatus: current.Status, CurrentStage: current.CurrentStage, RetryRevision: current.RetryRevision, NextStage: current.CurrentStage, NextStatus: "done"}); err != nil || rows != 1 {
			t.Fatalf("stored run transition: rows=%d err=%v", rows, err)
		}
	}
	{
		current, err := workflowsql.New(store.db.SQL()).StepByID(context.Background(), step)
		if err != nil {
			t.Fatal(err)
		}
		if rows, err := workflowsql.New(store.db.SQL()).RecordApproval(context.Background(), workflowsql.RecordApprovalParams{At: time.Now(), ID: step, Attempt: current.Attempt, ExpectedStatus: current.Status, Status: "pass", EvidenceJson: current.EvidenceJson}); err != nil || rows != 1 {
			t.Fatalf("stored step status: rows=%d err=%v", rows, err)
		}
	}
	again, err := store.create(context.Background(), req, def)
	if err != nil || again.ID != runIDs[0] {
		t.Fatalf("completed replay: %+v %v", again, err)
	}
	expect(t, storedStep(t, store.db, step).Status == StatusPass, "completed call was reopened")
}

func TestRunStartStoreConcurrentAcceptedLanding(t *testing.T) {
	t.Parallel()
	admissions, req, def := runAdmissionFixture(t)
	store := admissions.records
	work, err := store.create(context.Background(), req, def)
	if err != nil {
		t.Fatal(err)
	}
	{
		current, err := workflowsql.New(store.db.SQL()).RunByID(context.Background(), workflowsql.RunByIDParams{ID: work.ID})
		if err != nil {
			t.Fatal(err)
		}
		if rows, err := workflowsql.New(store.db.SQL()).TransitionRun(context.Background(), workflowsql.TransitionRunParams{At: time.Now(), ID: work.ID, ExpectedStatus: current.Status, CurrentStage: current.CurrentStage, RetryRevision: current.RetryRevision, NextStage: current.CurrentStage, NextStatus: "done"}); err != nil || rows != 1 {
			t.Fatalf("stored run transition: rows=%d err=%v", rows, err)
		}
	}
	req.AcceptedRunID = work.ID
	def.Spec.Role = "landing"
	runIDs := make([]ids.ID, 8)
	race(8, func(i int) int {
		run, err := store.create(context.Background(), req, def)
		if err != nil {
			t.Error(err)
			return 1
		}
		runIDs[i] = run.ID
		return 0
	})
	for _, id := range runIDs {
		if id.IsZero() || id != runIDs[0] {
			t.Fatalf("landing runs: %v", runIDs)
		}
	}
	// sql-guarantee: concurrent landings create exactly one landing run anywhere.
	completionCount(t, store.db, 2, `SELECT COUNT(*) FROM runs`)
	landing, ok := seenLanding(t, store.db, work.ID)
	expect(t, ok && landing.LandingRunID == runIDs[0], "landing reservation: %v %+v", ok, landing)
	ch := seenChange(t, store.db, req.ChangeID)
	expect(t, ch.RunID == runIDs[0] && ch.State == "landing", "change: %d %s", ch.RunID, ch.State)
}

func TestRunStartRejectsChangedAdoptionBeforeWriting(t *testing.T) {
	t.Parallel()
	admissions, req, def := runAdmissionFixture(t)
	store := admissions.records
	if err := store.db.InTx(context.Background(), func(tx *database.Tx) error {
		return adoption.RecordIn(t.Context(), tx, req.ProjectID, 1, adoption.AdoptionEdit, "moved", adoption.EmptyReceipt(), "alice", time.Now())
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.create(context.Background(), req, def); err == nil || !strings.Contains(err.Error(), "changed while starting") {
		t.Fatalf("adoption race: %v", err)
	}
	// sql-guarantee: a refused admission writes no run anywhere.
	completionCount(t, store.db, 0, `SELECT COUNT(*) FROM runs`)
}

func TestRunStartRetriedCallCreatesNewChildAttempt(t *testing.T) {
	t.Parallel()
	admissions, req, def := runAdmissionFixture(t)
	store := admissions.records
	def.Spec.Stages[0].Steps = []schema.Step{{Name: "call", Uses: "workflow/call"}}
	parent, err := store.create(context.Background(), req, def)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.prepared(context.Background(), parent.ID); err != nil {
		t.Fatal(err)
	}
	step := admittedStepID(t, queries{db: store.db}, parent, "call")
	req.ParentRunID, req.ParentStepID = parent.ID, step
	child, err := store.create(context.Background(), req, def)
	if err != nil {
		t.Fatal(err)
	}
	if child.ParentStepAttempt != 1 {
		t.Fatalf("first child attempt: %d", child.ParentStepAttempt)
	}
	// Completing a child requires its stage checks to have finished.
	{
		rows, err := workflowsql.New(store.db.SQL()).RunSteps(context.Background(), workflowsql.RunStepsParams{RunID: child.ID})
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) == 0 {
			t.Fatal("missing stage checks")
		}
		for _, step := range rows {
			if err := requireAttemptWrite(workflowsql.New(store.db.SQL()).RecordApproval(context.Background(), workflowsql.RecordApprovalParams{At: time.Now(), ID: step.ID, Attempt: step.Attempt, ExpectedStatus: step.Status, Status: StatusPass.String(), EvidenceJson: step.EvidenceJson})); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := (advanceStore{db: store.db, completion: completionRecorderFunc(noCompletionEffects)}).complete(context.Background(), child); err != nil {
		t.Fatal(err)
	}
	if _, err := newStepRetries(store.db, []string{"workflow/call"}).retryExpected(context.Background(), retryIntent{RetryCommand: RetryCommand{StepID: step}, Actor: commandActor{Receipt: "alice"}}, nil); err != nil {
		t.Fatal(err)
	}
	req.ParentStepAttempt = child.ParentStepAttempt
	second, err := store.create(context.Background(), req, def)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID == child.ID || second.ParentStepAttempt != 2 {
		t.Fatalf("retry reused old invocation: %+v", second)
	}
	again, err := store.create(context.Background(), req, def)
	if err != nil || again.ID != second.ID {
		t.Fatalf("retry replay: %+v %v", again, err)
	}
	call := storedStep(t, store.db, step)
	expect(t, call.Status == StatusRunning && call.Attempt == 2 && jsonMatches(t, call.Evidence, fmt.Sprintf("childRunId=%d", second.ID)), "retried call: %s %d %s", call.Status, call.Attempt, call.Evidence)
	expect(t, eventCount(t, store.db, parent.ID, "step.started", "step=call") == 2, "call starts")
}

func TestRunStartChildConfigPreservesExactIdentity(t *testing.T) {
	t.Parallel()
	for _, changed := range []bool{false, true} {
		t.Run(fmt.Sprint(changed), func(t *testing.T) {
			admissions, req, def := runAdmissionFixture(t)
			store := admissions.records
			parent, err := store.create(context.Background(), req, def)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.prepared(context.Background(), parent.ID); err != nil {
				t.Fatal(err)
			}
			id := insertStoredStep(t, store.db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: parent.ID, Stage: "first", Name: "call", Type: "workflow/call", ConfigJson: []byte(`{"workflow":"child","value":9007199254740993}`), Status: StatusPending.String()})
			step, err := readStep(context.Background(), store.db, id)
			if err != nil {
				t.Fatal(err)
			}
			req.ParentRunID, req.ParentStepID = parent.ID, id
			req.ParentStepConfig = string(step.Config)
			if changed {
				if err := requireAttemptWrite(workflowsql.New(store.db.SQL()).RetryStep(context.Background(), workflowsql.RetryStepParams{ID: id, Attempt: step.Attempt, ConfigJson: []byte(`{"workflow":"child","value":9007199254740992}`)})); err != nil {
					t.Fatal(err)
				}
			}
			child, err := store.create(context.Background(), req, def)
			if changed {
				if child != nil || err == nil || !strings.Contains(err.Error(), "no longer runnable") {
					t.Fatalf("changed exact configuration was accepted: %+v %v", child, err)
				}
				// sql-guarantee: no child of this call exists under any change; no owner read lists runs by parent step.
				completionCount(t, store.db, 0, `SELECT COUNT(*) FROM runs WHERE parent_step_id=$1`, id)
				call := storedStep(t, store.db, id)
				expect(t, call.Status == StatusPending && call.Attempt == 0, "refused call: %s %d", call.Status, call.Attempt)
			} else if err != nil || child == nil {
				t.Fatalf("JSON formatting rejected the snapshot: %+v %v", child, err)
			}
		})
	}
}

func TestRunStartRejectsSuppressedParentAdmission(t *testing.T) {
	t.Parallel()
	admissions, req, def := runAdmissionFixture(t)
	store := admissions.records
	def.Spec.Stages[0].Steps = []schema.Step{{Name: "call", Uses: "workflow/call"}}
	parent, err := store.create(context.Background(), req, def)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.prepared(context.Background(), parent.ID); err != nil {
		t.Fatal(err)
	}
	step := admittedStepID(t, queries{db: store.db}, parent, "call")
	req.ParentRunID, req.ParentStepID = parent.ID, step
	completionExec(t, store.db, `CREATE FUNCTION suppress_call() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.type='workflow/call' THEN RETURN NULL; END IF; RETURN NEW; END $$`)
	completionExec(t, store.db, `CREATE TRIGGER suppress_call BEFORE UPDATE ON steps FOR EACH ROW EXECUTE FUNCTION suppress_call()`)
	child, err := store.create(context.Background(), req, def)
	if child != nil || !errors.Is(err, ErrStepAttemptLost) {
		t.Fatalf("suppressed admission succeeded: %+v, %v", child, err)
	}
	// sql-guarantee: the parent is the only run anywhere, so its reads below cover every row.
	completionCount(t, store.db, 1, `SELECT COUNT(*) FROM runs`)
	_, started := seenRunStart(t, store.db, parent.ID)
	expect(t, started, "parent start record is missing")
	call := storedStep(t, store.db, step)
	expect(t, call.Status == StatusPending && call.Attempt == 0 && emptyJSON(call.Evidence), "suppressed call: %s %d %s", call.Status, call.Attempt, call.Evidence)
	expect(t, eventCount(t, store.db, parent.ID, "step.started,run.called") == 0, "suppressed admission left receipts")
}

func TestInferenceOwnerInheritedBeforeChildDispatch(t *testing.T) {
	t.Parallel()
	admissions, req, def := runAdmissionFixture(t)
	store := admissions.records
	def.Spec.Stages[0].Steps = []schema.Step{{Name: "call", Uses: "workflow/call"}}
	req.RequestedBy = "alice"
	parent, err := store.create(context.Background(), req, def)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.prepared(context.Background(), parent.ID); err != nil {
		t.Fatal(err)
	}
	step := admittedStepID(t, queries{db: store.db}, parent, "call")
	req.ParentRunID, req.ParentStepID, req.RequestedBy = parent.ID, step, "untrusted-child-actor"
	child, err := store.create(context.Background(), req, def)
	if err != nil {
		t.Fatal(err)
	}
	if owner := storedInferenceOwner(t, store.db, child.ID); owner != "alice" {
		t.Fatalf("child ownership %s", owner)
	}
}

func TestAbandonmentRacesRunCreation(t *testing.T) {
	t.Parallel()
	for _, child := range []bool{false, true} {
		t.Run(fmt.Sprint("child=", child), func(t *testing.T) {
			admissions, req, def := runAdmissionFixture(t)
			s := admissions.records
			stop := Abandonment{ChangeID: req.ChangeID}
			if child {
				def.Spec.Stages[0].Steps = []schema.Step{{Name: "call", Uses: "workflow/call"}}
				parent, err := s.create(context.Background(), req, def)
				if err != nil {
					t.Fatal(err)
				}
				if err := s.prepared(context.Background(), parent.ID); err != nil {
					t.Fatal(err)
				}
				req.ParentRunID = parent.ID
				req.ParentStepID = admittedStepID(t, queries{db: s.db}, parent, "call")
				stop = Abandonment{RunID: parent.ID}
			}
			race(2, func(i int) int {
				if i == 0 {
					_, err := s.create(context.Background(), req, def)
					if err != nil && !strings.Contains(err.Error(), "not active") && !strings.Contains(err.Error(), "abandoned") {
						t.Error(err)
					}
				} else {
					if _, err := NewAbandonments(s.db, abandonRecorderFunc(noAbandonEffects), &scm.RepositoryLocks{}).Abandon(context.Background(), stop); err != nil {
						t.Error(err)
					}
				}
				return 0
			})
			// sql-guarantee: no run anywhere survives abandonment still running.
			completionCount(t, s.db, 0, `SELECT COUNT(*) FROM runs WHERE status='running'`)
		})
	}
}

// race runs n copies of f started on one signal and returns their results.
func race(n int, f func(i int) int) []int {
	out := make([]int, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			out[i] = f(i)
		}(i)
	}
	close(start)
	wg.Wait()
	return out
}

func count(codes []int, want int) int {
	n := 0
	for _, c := range codes {
		if c == want {
			n++
		}
	}
	return n
}
