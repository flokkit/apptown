package workflow

import (
	"context"
	"errors"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/database"
)

func TestChangeTransitionSuppressionRollsBackOwningWork(t *testing.T) {
	t.Parallel()
	for _, act := range []string{"start run", "land", "send back", "abandon change", "cancel landing", "ship"} {
		t.Run(act, func(t *testing.T) {
			t.Parallel()
			var db *database.DB
			var changeID ids.ID
			var perform func() error
			switch act {
			case "start run", "land":
				admissions, req, def := runAdmissionFixture(t)
				s := admissions.records
				if act == "land" {
					def.Spec.Role = "landing"
				}
				db, changeID = s.db, req.ChangeID
				perform = func() error { _, err := s.create(context.Background(), req, def); return err }
			case "send back":
				s, run, _ := completedSendBackFixture(t)
				db, changeID = s.db, run.ChangeID
				perform = func() error {
					_, err := s.sendBack(context.Background(), run.ChangeID, "preserve this conversation", "alice", "", run.ProjectID)
					return err
				}
			default:
				s, run, _ := completionFixture(t, "landing")
				db, changeID = s, run.ChangeID
				transitionStoredChangeForTest(t, db, changeID, ChangeLanding, false)
				if act == "ship" {
					perform = func() error {
						return (advanceStore{db: s, completion: completionRecorderFunc(noCompletionEffects)}).complete(context.Background(), run)
					}
				} else {
					req := Abandonment{Who: "alice", RunID: run.ID}
					if act == "abandon change" {
						req.RunID, req.ChangeID = ids.Nil, run.ChangeID
					}
					perform = func() error {
						_, err := NewAbandonments(db, abandonRecorderFunc(noAbandonEffects), &scm.RepositoryLocks{}).Abandon(context.Background(), req)
						return err
					}
				}
			}
			completionExec(t, db, `CREATE FUNCTION veto_change_transition() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NULL; END $$`)
			completionExec(t, db, `CREATE TRIGGER veto_change_transition BEFORE UPDATE ON changes FOR EACH ROW EXECUTE FUNCTION veto_change_transition()`)
			before := completionSnapshot(t, db)
			changeBefore, err := readChange(context.Background(), db, changeID)
			if err != nil {
				t.Fatal(err)
			}
			if err := perform(); !errors.Is(err, ErrChangeTransition) {
				t.Fatalf("suppressed %s succeeded: %v", act, err)
			}
			if after := completionSnapshot(t, db); after != before {
				t.Fatal("rejected Change transition left Work facts or receipts")
			}
			changeAfter, err := readChange(context.Background(), db, changeID)
			if err != nil || *changeAfter != *changeBefore {
				t.Fatalf("Change changed: %+v %v", changeAfter, err)
			}
			for _, table := range []string{"workflow_continuations", "workflow_landings"} {
				// sql-guarantee: no continuation link or landing reservation may exist under any run; no owner read enumerates them.
				completionCount(t, db, 0, `SELECT COUNT(*) FROM `+table)
			}
			expect(t, auditCount(t, db, "", nil) == 0, "rejected transition left a receipt")
			completionExec(t, db, `DROP TRIGGER veto_change_transition ON changes`)
			if err := perform(); err != nil {
				t.Fatalf("explicit rerun after suppressed %s: %v", act, err)
			}
		})
	}
}
