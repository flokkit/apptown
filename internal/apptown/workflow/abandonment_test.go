package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
)

type abandonRecorderFunc func(context.Context, *database.Tx, AbandonEffects) error

func (f abandonRecorderFunc) RecordAbandonIn(ctx context.Context, tx *database.Tx, in AbandonEffects) error {
	return f(ctx, tx, in)
}
func noAbandonEffects(context.Context, *database.Tx, AbandonEffects) error { return nil }
func abandonFixture(t *testing.T) (abandonments, *Run) {
	t.Helper()
	admissions, req, def := runAdmissionFixture(t)
	run, err := admissions.records.create(context.Background(), req, def)
	if err != nil {
		t.Fatal(err)
	}
	return NewAbandonments(admissions.records.db, abandonRecorderFunc(noAbandonEffects), &scm.RepositoryLocks{}), run
}
func insertAbandonChild(t *testing.T, db *database.DB, project, parent ids.ID, state RunState) ids.ID {
	t.Helper()
	return insertStoredRun(t, db, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: project, ParentRunID: parent, WorkflowName: "child", WorkflowVersion: 1, Kind: "pipeline", Status: state.String(), CurrentStage: "first", DefinitionJson: []byte(`{}`)}).ID
}
func TestAbandonmentTraversesFinishedHistoryWithoutCrossingProjectScope(t *testing.T) {
	t.Parallel()
	for _, change := range []bool{false, true} {
		t.Run(fmt.Sprint(change), func(t *testing.T) {
			t.Parallel()
			owner, root := abandonFixture(t)
			db := owner.db
			done := insertAbandonChild(t, db, root.ProjectID, root.ID, RunDone)
			active := insertAbandonChild(t, db, root.ProjectID, done, RunRunning)
			foreignProject := workflowTestProject(t, db, "foreign", "Foreign", "")
			foreign := insertAbandonChild(t, db, foreignProject, root.ID, RunRunning)
			throughForeign := insertAbandonChild(t, db, root.ProjectID, foreign, RunRunning)
			in := Abandonment{RunID: root.ID, Who: "operator", Reason: "stop"}
			if change {
				in.RunID = ids.Nil
				in.ChangeID = root.ChangeID
			}
			result, err := owner.Abandon(context.Background(), in)
			if err != nil || len(result.RunIDs) != 2 || result.RunIDs[0] != root.ID || result.RunIDs[1] != active {
				t.Fatalf("owned tree: %+v %v", result, err)
			}
			wantRunState(t, db, done, RunDone)
			wantRunState(t, db, foreign, RunRunning)
			wantRunState(t, db, throughForeign, RunRunning)
			wantEventCount(t, db, 2, kindWith("run.abandoned", "by", "operator"))
		})
	}
}
func TestAbandonmentRequiredParticipantsCommitTogether(t *testing.T) {
	t.Parallel()
	for _, fail := range []string{"effects", "run receipt", "audit", "suppressed transition"} {
		t.Run(fail, func(t *testing.T) {
			t.Parallel()
			owner, root := abandonFixture(t)
			db := owner.db
			switch fail {
			case "effects":
				owner.effects = abandonRecorderFunc(func(context.Context, *database.Tx, AbandonEffects) error { return errors.New("effect unavailable") })
			case "run receipt":
				dbtest.FailNextStatement(t, db, "INSERT INTO run_events")
			case "audit":
				dbtest.FailNextStatement(t, db, "INSERT INTO audit_log")
			case "suppressed transition":
				completionExec(t, db, `CREATE FUNCTION veto_work_stop() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.status='abandoned' THEN RETURN NULL; END IF; RETURN NEW; END $$`)
				completionExec(t, db, `CREATE TRIGGER veto_work_stop BEFORE UPDATE ON runs FOR EACH ROW EXECUTE FUNCTION veto_work_stop()`)
			}
			out, err := owner.Abandon(context.Background(), Abandonment{ChangeID: root.ChangeID, Who: "operator"})
			if out != nil || err == nil {
				t.Fatalf("partial %s: %+v %v", fail, out, err)
			}
			wantRunState(t, db, root.ID, RunRunning)
			wantChangeState(t, db, root.ChangeID, ChangeInProgress)
			wantEventCount(t, db, 0, ofKind("run.abandoned"))
			wantReceiptCount(t, db, 0, receiptKind("change.abandoned", "run.abandoned"))
		})
	}
}

func TestAbandonmentRollsBackEarlierSiblings(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"constraint", "suppressed transition"} {
		for _, change := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/change=%t", failure, change), func(t *testing.T) {
				t.Parallel()
				owner, root := abandonFixture(t)
				db := owner.db
				child := insertAbandonChild(t, db, root.ProjectID, root.ID, RunRunning)
				if failure == "constraint" {
					completionExec(t, db, fmt.Sprintf(`ALTER TABLE runs ADD CONSTRAINT reject_child_abandon CHECK (id!='%s' OR status != 'abandoned')`, child))
				} else {
					completionExec(t, db, fmt.Sprintf(`CREATE FUNCTION suppress_abandon() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.id='%s' AND NEW.status='abandoned' THEN RETURN NULL; END IF; RETURN NEW; END $$`, child))
					completionExec(t, db, `CREATE TRIGGER suppress_abandon BEFORE UPDATE ON runs FOR EACH ROW EXECUTE FUNCTION suppress_abandon()`)
				}
				req := Abandonment{RunID: root.ID}
				if change {
					req.RunID = ids.Nil
					req.ChangeID = root.ChangeID
				}
				if _, err := owner.Abandon(context.Background(), req); err == nil {
					t.Fatal("child write should fail")
				} else if failure == "suppressed transition" && !errors.Is(err, ErrRunMoved) {
					t.Fatalf("suppressed child: %v", err)
				}
				wantRunCount(t, db, 2, func(r *Run) bool { return r.Status == RunRunning })
				// sql-guarantee: a whole-store total across every project; no workflow read enumerates Changes unscoped, and rollback must leave exactly the one open Change.
				completionCount(t, db, 1, `SELECT COUNT(*) FROM changes WHERE state='in_progress' AND closed_at IS NULL`)
				// Admission records two events; failure must preserve the committed baseline.
				wantEventCount(t, db, 2, anyEvent)
				wantReceiptCount(t, db, 0, nil)
			})
		}
	}

}

func TestAbandonmentScopeIsRecheckedAfterAuthorityAndBeforeEffects(t *testing.T) {
	t.Parallel()
	for _, change := range []bool{false, true} {
		t.Run(fmt.Sprint(change), func(t *testing.T) {
			t.Parallel()
			owner, root := abandonFixture(t)
			db := owner.db
			other := workflowTestProject(t, db, "foreign", "Foreign", "")
			called := false
			recorder := abandonRecorderFunc(func(context.Context, *database.Tx, AbandonEffects) error { called = true; return nil })
			auth := actorAuthorityFunc(func(_ context.Context, p operationPermission) (commandActor, error) {
				actor := commandActor{Receipt: "verified", InferenceOwner: "verified"}
				if p.Action == actionIdentify {
					return actor, nil
				}
				expected := actionStopRun
				if change {
					expected = actionCloseChange
				}
				if p.Action != expected || p.ProjectID != root.ProjectID {
					t.Fatalf("scope: %+v", p)
				}
				if change {
					// sql-guarantee: Move the authorized Change to another project inside the authority callback; no command may rehome its immutable identity.
					completionExec(t, db, `UPDATE changes SET project_id=$1,number=number+100000 WHERE id=$2`, other, root.ChangeID)
				} else {
					// sql-guarantee: No owner mutation rehomes an existing run's project/Change binding; replace that identity after authorization to fence its effects.
					completionExec(t, db, `UPDATE runs SET project_id=$1,number=number+100000,change_id=NULL WHERE id=$2`, other, root.ID)
				}
				return actor, nil
			})
			cmd := StopWork{RunID: root.ID}
			if change {
				cmd.RunID = ids.Nil
				cmd.ChangeID = root.ChangeID
			}
			out, err := newService(db, auth, Runtime{Abandonment: recorder}).Abandon(context.Background(), cmd)
			if out != nil || !errors.Is(err, sql.ErrNoRows) || called {
				t.Fatalf("moved scope: %+v %v effects=%v", out, err, called)
			}
			wantRunState(t, db, root.ID, RunRunning)
		})
	}
}
func TestAbandonmentRequiresCallerAndUsesVerifiedReceiptActor(t *testing.T) {
	t.Parallel()
	if out, err := newService(nil, nil, Runtime{}).Abandon(context.Background(), StopWork{RunID: idstest.N(1)}); out != nil || !errors.Is(err, ErrCallerRequired) {
		t.Fatalf("no authority: %+v %v", out, err)
	}
	owner, root := abandonFixture(t)
	db := owner.db
	auth := actorAuthorityFunc(func(context.Context, operationPermission) (commandActor, error) {
		return commandActor{Receipt: "verified"}, &access.Denied{Reason: "stop denied"}
	})
	service := newService(db, auth, Runtime{Abandonment: abandonRecorderFunc(noAbandonEffects)})
	if out, err := service.Abandon(context.Background(), StopWork{RunID: root.ID}); out != nil || err == nil {
		t.Fatalf("denied: %+v %v", out, err)
	}
	service.authority = allowProject(root.ProjectID)
	if _, err := service.Abandon(context.Background(), StopWork{RunID: root.ID, Reason: "done here"}); err != nil {
		t.Fatal(err)
	}
	wantEventCount(t, db, 1, kindWith("run.abandoned", "by", "verified-person", "reason", "done here"))
}
func TestAbandonmentCancelsProjectLockWait(t *testing.T) {
	t.Parallel()
	owner, root := abandonFixture(t)
	db := owner.db
	gate, err := db.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = gate.Rollback() }()
	if _, err := workflowsql.New(gate.SQL()).ProjectForUpdate(context.Background(), root.ProjectID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := owner.Abandon(ctx, Abandonment{RunID: root.ID}); done <- err }()
	dbtest.WaitForLock(t, db, "-- name: ProjectForUpdate")
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("abandonment ignored cancellation")
	}
	wantRunState(t, db, root.ID, RunRunning)
}

func TestFailedLandingSupersessionRechecksCurrentWork(t *testing.T) {
	t.Parallel()
	for _, change := range []string{"none", "resumed", "passed", "role", "read failure"} {
		t.Run(change, func(t *testing.T) {
			t.Parallel()
			admissions, req, def := runAdmissionFixture(t)
			db := admissions.records.db
			ctx := context.Background()
			pinStartFixture(t, admissions, req, &def)
			def.Spec.Role = "landing"
			run, err := admissions.records.create(ctx, req, def)
			if err != nil {
				t.Fatal(err)
			}
			step := insertStoredStep(t, db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "first", Name: "failed", Type: "script", ConfigJson: []byte(`{}`), Status: StatusFail.String()})
			source, err := admissions.Source(ctx, req)
			if err != nil || len(source.Superseded) != 1 {
				t.Fatalf("candidate: %+v %v", source, err)
			}
			switch change {
			case "resumed":
				{
					current, err := workflowsql.New(db.SQL()).StepByID(context.Background(), step)
					if err != nil {
						t.Fatal(err)
					}
					if rows, err := workflowsql.New(db.SQL()).RecordApproval(context.Background(), workflowsql.RecordApprovalParams{At: time.Now(), ID: step, Attempt: current.Attempt, ExpectedStatus: current.Status, Status: "running", EvidenceJson: current.EvidenceJson}); err != nil || rows != 1 {
						t.Fatalf("stored step status: rows=%d err=%v", rows, err)
					}
				}
			case "passed":
				{
					current, err := workflowsql.New(db.SQL()).StepByID(context.Background(), step)
					if err != nil {
						t.Fatal(err)
					}
					if rows, err := workflowsql.New(db.SQL()).RecordApproval(context.Background(), workflowsql.RecordApprovalParams{At: time.Now(), ID: step, Attempt: current.Attempt, ExpectedStatus: current.Status, Status: "pass", EvidenceJson: current.EvidenceJson}); err != nil || rows != 1 {
						t.Fatalf("stored step status: rows=%d err=%v", rows, err)
					}
				}
			case "role":
				// sql-guarantee: Change the pinned landing role after supersession selection; admission cannot rewrite a selected run's declaration.
				completionExec(t, db, `UPDATE runs SET definition_json=jsonb_set(definition_json,'{role}','"working"') WHERE id=$1`, run.ID)
			case "read failure":
				dbtest.FailNextStatement(t, db, "-- name: AbandonFailedLandingEligible")
			}
			out, err := NewAbandonments(db, abandonRecorderFunc(noAbandonEffects), &scm.RepositoryLocks{}).supersedeFailedLanding(ctx, source.Superseded[0], "work")
			if change == "none" {
				if err != nil || len(out.RunIDs) != 1 {
					t.Fatalf("failed landing: %+v %v", out, err)
				}
				wantChangeState(t, db, req.ChangeID, ChangeInProgress)
			} else {
				if err == nil || out != nil {
					t.Fatalf("stale %s superseded: %+v %v", change, out, err)
				}
				wantRunState(t, db, run.ID, RunRunning)
				wantEventCount(t, db, 0, ofKind("run.abandoned"))
			}
		})
	}
}

// What abandonment does to the change follows its target and the run's
// role. Stopping a working run's root leaves the change in progress;
// stopping a landing's root cancels the landing, unless the work is kept
// for a chat; stopping a child stops only its subtree and leaves the root
// and the change alone; abandoning the change stops all its trees and
// closes it. A finished run and another change's run are never touched.
// The effects AppTown records with it (ticket revocation and cleanup when the
// change closes, a narrative when a root run stops) are told which.
func TestAbandonmentSettlesTheChangeByItsTargetAndRole(t *testing.T) {
	t.Parallel()
	for _, target := range []string{"working", "landing", "child", "change"} {
		t.Run(target, func(t *testing.T) {
			t.Parallel()
			role := "landing"
			if target == "working" {
				role = "working"
			}
			db, root, _ := completionFixture(t, role)
			if role == "landing" {
				transitionStoredChangeForTest(t, db, root.ChangeID, ChangeLanding, false)
			}
			// Called runs carry their root's change, as Work admits them.
			called := func(parent ids.ID, state RunState) ids.ID {
				return insertStoredRun(t, db, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: root.ProjectID, ChangeID: root.ChangeID, ParentRunID: parent, WorkflowName: "child", WorkflowVersion: 1, Kind: "pipeline", Status: state.String(), CurrentStage: "first", DefinitionJson: []byte(`{}`)}).ID
			}
			child := called(root.ID, RunRunning)
			done := called(child, RunDone)
			grandchild := called(child, RunRunning)
			otherChange := admissionTestChange(t, db, root.ProjectID, "Other")
			other := insertStoredRun(t, db, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: root.ProjectID, ChangeID: otherChange, WorkflowName: "other", WorkflowVersion: 1, Kind: "pipeline", Status: RunRunning.String(), CurrentStage: "finish", DefinitionJson: []byte(`{"kind":"pipeline","role":"working","stages":[{"name":"finish"}]}`)}).ID
			var effects []AbandonEffects
			owner := NewAbandonments(db, abandonRecorderFunc(func(_ context.Context, _ *database.Tx, in AbandonEffects) error {
				effects = append(effects, in)
				return nil
			}), &scm.RepositoryLocks{})
			req := Abandonment{RunID: root.ID, Who: "alice"}
			switch target {
			case "child":
				req.RunID = child
			case "change":
				req.RunID, req.ChangeID = ids.Nil, root.ChangeID
			}
			result, err := owner.Abandon(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			want := []ids.ID{root.ID, child, grandchild}
			if target == "child" {
				want = []ids.ID{child, grandchild}
			}
			slices.SortFunc(result.RunIDs, ids.ID.Compare)
			if !slices.Equal(result.RunIDs, want) {
				t.Fatalf("abandoned %v, want %v", result.RunIDs, want)
			}
			if target == "child" {
				wantRunState(t, db, root.ID, RunRunning)
			} else {
				wantRunState(t, db, root.ID, RunAbandoned)
			}
			wantRunState(t, db, child, RunAbandoned)
			wantRunState(t, db, grandchild, RunAbandoned)
			wantRunState(t, db, done, RunDone)
			wantRunState(t, db, other, RunRunning)
			state := map[string]ChangeState{"working": ChangeInProgress, "landing": ChangeInProgress, "child": ChangeLanding, "change": ChangeAbandoned}[target]
			if ch := wantChangeState(t, db, root.ChangeID, state); ch.ClosedAt.Valid != (target == "change") {
				t.Fatalf("change closed: %v", ch.ClosedAt)
			}
			wantChangeState(t, db, otherChange, ChangeInProgress)
			stoppedRoot := target != "child" && target != "change"
			if len(effects) != 1 || effects[0].ChangeID != root.ChangeID || effects[0].CloseChange != (target == "change") || effects[0].Root != stoppedRoot {
				t.Fatalf("effects: %+v", effects)
			}
		})
	}
}

// A change closed as nothing to change says so only when AppTown's own
// close recorded it: the same words as a person's reason leave an
// abandonment, and AppTown's close records the fact with the change's
// state and its receipt.
//
// Kills: a label read from the reason's words; a closedAs a caller can
// set; a close whose receipt does not carry what the change records.
func TestOnlyAppTownsCloseRecordsNothingToChange(t *testing.T) {
	t.Parallel()
	const reason = "Nothing to update: the project declares no dependencies"
	for _, c := range []struct {
		name string
		req  func(Abandonment) Abandonment
		want string
	}{
		{"a person with the same words", func(r Abandonment) Abandonment { r.Who = "operator"; return r }, ""},
		{"AppTown's nothing-to-change close", func(r Abandonment) Abandonment {
			r.Who, r.closedAs = "apptown-system", ClosedNothingToChange
			return r
		}, ClosedNothingToChange},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			owner, root := abandonFixture(t)
			if _, err := owner.Abandon(t.Context(), c.req(Abandonment{ChangeID: root.ChangeID, Reason: reason})); err != nil {
				t.Fatal(err)
			}
			ch, err := ReadChangeIn(t.Context(), owner.db, root.ChangeID)
			if err != nil || ch.State != ChangeAbandoned || ch.ClosedAs != c.want || ch.AbandonReason != reason {
				t.Fatalf("change: %+v %v", ch, err)
			}
			receipts, err := audit.Recent(t.Context(), owner.db, audit.Filter{ProjectID: ch.ProjectID, KindPrefix: "change.abandoned", Limit: 1})
			if err != nil || len(receipts) != 1 {
				t.Fatalf("receipts: %+v %v", receipts, err)
			}
			var receipt abandonAudit
			if err := json.Unmarshal(receipts[0].Payload, &receipt); err != nil || receipt.ClosedAs != c.want {
				t.Fatalf("receipt: %s %v", receipts[0].Payload, err)
			}
		})
	}
}
