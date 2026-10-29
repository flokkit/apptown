package workflow

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

type completionRecorderFunc func(context.Context, *database.Tx, CompletedRun) (ReleaseReceipt, error)

func (f completionRecorderFunc) RecordCompletionIn(ctx context.Context, tx *database.Tx, in CompletedRun) (ReleaseReceipt, error) {
	return f(ctx, tx, in)
}
func (f completionRecorderFunc) RetryCompletionIn(context.Context, *database.Tx, ids.ID) (bool, error) {
	return false, nil
}
func TestCompletionOwnerRequiresPinnedSnapshotAndAllParticipants(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"modified definition", "port failure", "no release receipt", "success"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			q, run, id := activityFixture(t)
			transitionStoredStepForTest(t, q.db, id, StatusPass)
			if failure == "no release receipt" {
				// sql-guarantee: the no-release-receipt branch needs the same receipt-free stored run to become a landing declaration; no generated mutation changes definition_json.
				completionExec(t, q.db, `UPDATE runs SET definition_json='{"kind":"pipeline","role":"landing","stages":[{"name":"finish"}]}' WHERE id=$1`, run.ID)
				// sql-guarantee: the no-release-receipt branch needs the same receipt-free step to become a deploy declaration; no generated mutation changes type and config together.
				completionExec(t, q.db, `UPDATE steps SET type='deploy',config_json='{"deployment":"main"}' WHERE id=$1`, id)
				var err error
				run, err = readRun(context.Background(), q.db, run.ID)
				if err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			recorder := completionRecorderFunc(func(ctx context.Context, tx *database.Tx, in CompletedRun) (ReleaseReceipt, error) {
				calls++
				if in.RunID != run.ID || in.ProjectID != run.ProjectID || in.ChangeID != run.ChangeID {
					t.Fatalf("scope %+v", in)
				}
				if !in.Release && !reflect.DeepEqual(in.Effects, []CompletionEffect{CompletionCleanup, CompletionLand}) || !in.Narrative {
					t.Fatalf("effects %+v", in.Effects)
				}
				if err := recordReceiptIn(ctx, tx, in.RunID, "test.external.receipt", struct{}{}, time.Now()); err != nil {
					return ReleaseReceipt{}, err
				}
				if failure == "port failure" {
					return ReleaseReceipt{}, errors.New("external record failed")
				}
				return ReleaseReceipt{}, nil
			})
			owner := advanceStore{db: q.db, completion: recorder}
			if failure == "modified definition" {
				run.DefinitionJSON = `{"kind":"pipeline","role":"landing","stages":[{"name":"finish"}]}`
			}
			err := owner.complete(context.Background(), run)
			if failure == "success" {
				if err != nil || calls != 1 {
					t.Fatalf("completion calls=%d %v", calls, err)
				}
				wantEventCount(t, q.db, 1, ofKind("test.external.receipt"))
				wantRunCount(t, q.db, 1, func(r *Run) bool { return r.Status == RunDone })
				if err := owner.complete(context.Background(), run); !errors.Is(err, ErrRunMoved) {
					t.Fatalf("repeated settlement %v", err)
				}
				if calls != 1 {
					t.Fatal("repeated effects")
				}
				return
			}
			if err == nil {
				t.Fatal("partial or modified completion succeeded")
			}
			if failure == "modified definition" && calls != 0 {
				t.Fatal("modified declaration reached participant")
			}
			wantEventCount(t, q.db, 0, anyEvent)
			wantRunCount(t, q.db, 1, func(r *Run) bool { return r.Status == RunRunning })
			// sql-guarantee: no workflow read enumerates landing links; the total proves completion reserved none.
			completionCount(t, q.db, 0, `SELECT COUNT(*) FROM workflow_landings`)
		})
	}
}
func TestCompletionChildRequiresEveryInheritedArtifact(t *testing.T) {
	t.Parallel()
	q, parent, _ := activityFixture(t)
	call := insertStoredCall(t, q.db, parent.ID, "finish", "child", 2, []byte(`{}`))
	child := insertStoredRun(t, q.db, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: parent.ProjectID, WorkflowName: "child", WorkflowVersion: 1, Kind: "pipeline", CurrentStage: "finish", DefinitionJson: []byte(parent.DefinitionJSON), Status: RunRunning.String(), ParentRunID: parent.ID, ParentStepID: call})
	if changed, err := workflowsql.New(q.db.SQL()).SetParentStepAttempt(context.Background(), workflowsql.SetParentStepAttemptParams{ParentStepAttempt: 2, ID: child.ID}); err != nil || changed != 1 {
		t.Fatalf("store child attempt: changed=%d %v", changed, err)
	}
	recordStoredCallEvidence(t, q.db, call, 2, []byte(fmt.Sprintf(`{"childRunId":%q,"workflow":"child"}`, child.ID)))
	insertBinaryArtifactForTest(t, q.db, child.ID, "preview", "image", "", []byte{0, 255, 42}, time.Time{})
	completionExec(t, q.db, `CREATE FUNCTION veto_child_copy() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NULL; END $$`)
	completionExec(t, q.db, `CREATE TRIGGER veto_child_copy BEFORE INSERT ON artifacts FOR EACH ROW EXECUTE FUNCTION veto_child_copy()`)
	owner := advanceStore{db: q.db, completion: completionRecorderFunc(func(context.Context, *database.Tx, CompletedRun) (ReleaseReceipt, error) {
		t.Fatal("incomplete child reached effects")
		return ReleaseReceipt{}, nil
	})}
	if err := owner.complete(context.Background(), child); err == nil {
		t.Fatal("child completed without its inherited artifact")
	}
	wantRunState(t, q.db, child.ID, RunRunning)
	wantStepState(t, q.db, call, StatusRunning)
	wantEventCount(t, q.db, 0, anyEvent)
}

// A record run (research, a specification) keeps records, not commits:
// accepting it closes its change, and nothing lands — no landing is
// queued, no merge is asked of its last stage, and no release is cut (an
// accepted research question must not merge an empty branch and redeploy
// the app).
func TestARecordRunClosesItsChangeWithoutLanding(t *testing.T) {
	t.Parallel()
	q, working, _ := activityFixture(t)
	ctx := context.Background()
	record := insertStoredRun(t, q.db, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: working.ProjectID, ChangeID: working.ChangeID, WorkflowName: "research", WorkflowVersion: 1, Kind: "lifecycle", Status: RunRunning.String(), CurrentStage: "synthesize", DefinitionJson: []byte(`{"kind":"lifecycle","role":"record","stages":[{"name":"investigate"},{"name":"synthesize"}]}`)})
	spec, err := record.Definition()
	if err != nil {
		t.Fatal(err)
	}
	if next, err := nextStage(spec, "synthesize", false); err != nil || !next.Completing || next.Merge {
		t.Fatalf("a record run's last stage: %+v %v", next, err)
	}
	var effects []CompletionEffect
	recorder := completionRecorderFunc(func(_ context.Context, _ *database.Tx, in CompletedRun) (ReleaseReceipt, error) {
		effects = in.Effects
		if in.Release {
			t.Fatal("a record run cut a release")
		}
		return ReleaseReceipt{}, nil
	})
	if err := (advanceStore{db: q.db, completion: recorder}).complete(ctx, record); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(effects, []CompletionEffect{CompletionCleanup}) {
		t.Fatalf("a record run's completion effects: %v", effects)
	}
	ch, err := readChange(ctx, q.db, working.ChangeID)
	if err != nil || !ch.State.Closed() || ch.State != ChangeShipped || ch.ClosedAs != closedRecord || ch.StateLabel != "finished" {
		t.Fatalf("the change after its record was accepted: %+v %v", ch, err)
	}
	// The board and the lists read the same words: the fact is the change's.
	listed, err := readChangeList(ctx, q.db, working.ProjectID, ChangeList{})
	if err != nil || len(listed) != 1 || listed[0].StateLabel != "finished" {
		t.Fatalf("the change as listed: %+v %v", listed, err)
	}
	pending, err := (landings{db: q.db}).pending(ctx, record.ID)
	if err != nil || !pending.LandingID.IsZero() {
		t.Fatalf("a landing was queued for a record run: %+v %v", pending, err)
	}
}
