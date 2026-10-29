package workflow

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"

	"github.com/flokkit/apptown/internal/database"
)

func TestSupersededChangeSummaryChecksScopeStateAndWrite(t *testing.T) {
	t.Parallel()
	for _, test := range []string{"success", "project", "state", "empty reason", "suppressed"} {
		t.Run(test, func(t *testing.T) {
			t.Parallel()
			q, run, _ := activityFixture(t)
			{
				current, err := workflowsql.New(q.db.SQL()).ChangeByID(context.Background(), workflowsql.ChangeByIDParams{ID: run.ChangeID})
				if err != nil {
					t.Fatal(err)
				}
				if err := requireRunWrite(workflowsql.New(q.db.SQL()).TransitionChange(context.Background(), workflowsql.TransitionChangeParams{At: time.Now(), ID: run.ChangeID, ProjectID: current.ProjectID, OldState: current.State, OldRunID: current.RunID, State: ChangeAbandoned.String(), RunID: current.RunID, Branch: current.Branch})); err != nil {
					t.Fatal(err)
				}
			}
			if err := requireRunWrite(workflowsql.New(q.db.SQL()).RecordSupersededChange(context.Background(), workflowsql.RecordSupersededChangeParams{ID: run.ChangeID, ProjectID: run.ProjectID, State: ChangeAbandoned.String(), Summary: "prior"})); err != nil {
				t.Fatal(err)
			}
			in := SupersededChange{Change: ChangeRef{ID: run.ChangeID, ProjectID: run.ProjectID}, Reason: "superseded"}
			switch test {
			case "project":
				in.Change.ProjectID = idstest.N(999999)
			case "state":
				{
					current, err := workflowsql.New(q.db.SQL()).ChangeByID(context.Background(), workflowsql.ChangeByIDParams{ID: run.ChangeID})
					if err != nil {
						t.Fatal(err)
					}
					if rows, err := workflowsql.New(q.db.SQL()).TransitionChange(context.Background(), workflowsql.TransitionChangeParams{At: time.Now(), ID: run.ChangeID, ProjectID: current.ProjectID, OldState: current.State, OldRunID: current.RunID, State: "in_progress", RunID: current.RunID, Branch: current.Branch}); err != nil || rows != 1 {
						t.Fatalf("stored change transition: rows=%d err=%v", rows, err)
					}
				}
			case "empty reason":
				in.Reason = " "
			case "suppressed":
				completionExec(t, q.db, `CREATE FUNCTION veto_summary() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NULL; END $$`)
				completionExec(t, q.db, `CREATE TRIGGER veto_summary BEFORE UPDATE ON changes FOR EACH ROW EXECUTE FUNCTION veto_summary()`)
			}
			err := q.db.InTx(context.Background(), func(tx *database.Tx) error { return RecordSupersededChangeIn(context.Background(), tx, in) })
			if test == "success" {
				if err != nil {
					t.Fatal(err)
				}
				wantEqual(t, "change summary", storedChange(t, q.db, run.ChangeID).Summary, "superseded")
			} else {
				if !errors.Is(err, errChangeEditConflict) {
					t.Fatalf("invalid summary: %v", err)
				}
				wantEqual(t, "change summary", storedChange(t, q.db, run.ChangeID).Summary, "prior")
			}
		})
	}
}

func TestUpliftAdoptionRequiresShippedScopeBeforeReplay(t *testing.T) {
	t.Parallel()
	for _, test := range []string{"new", "replay", "project", "type", "state", "suppressed", "rollback"} {
		t.Run(test, func(t *testing.T) {
			t.Parallel()
			typ := "uplift"
			if test == "type" {
				typ = "feature"
			}
			q, run := storedChangeRunFixture(t, workflowsql.InsertChangeParams{ID: ids.New(time.Now()), CreatedAt: time.Now(), Type: typ, State: ChangeShipped.String()})
			ref := ChangeRef{ID: run.ChangeID, ProjectID: run.ProjectID}
			switch test {
			case "replay":
				if err := requireRunWrite(workflowsql.New(q.db.SQL()).StampUpliftAdoption(context.Background(), workflowsql.StampUpliftAdoptionParams{ID: run.ChangeID, ProjectID: run.ProjectID, State: ChangeShipped.String(), AdoptedRevision: 7})); err != nil {
					t.Fatal(err)
				}
			case "project":
				ref.ProjectID = idstest.N(999999)
			case "type":
				// The foreign type was selected at insertion; a persisted adoption stamp
				// on a feature is intentionally impossible through StampUpliftAdoption.
				// sql-guarantee: A feature carrying an adoption stamp must still fail the type guard; StampUpliftAdoption only stamps uplift rows.
				completionExec(t, q.db, `UPDATE changes SET adopted_revision=7 WHERE id=$1`, run.ChangeID)
			case "state":
				if err := requireRunWrite(workflowsql.New(q.db.SQL()).StampUpliftAdoption(context.Background(), workflowsql.StampUpliftAdoptionParams{ID: run.ChangeID, ProjectID: run.ProjectID, State: ChangeShipped.String(), AdoptedRevision: 7})); err != nil {
					t.Fatal(err)
				}
				{
					current, err := workflowsql.New(q.db.SQL()).ChangeByID(context.Background(), workflowsql.ChangeByIDParams{ID: run.ChangeID})
					if err != nil {
						t.Fatal(err)
					}
					if err := requireRunWrite(workflowsql.New(q.db.SQL()).TransitionChange(context.Background(), workflowsql.TransitionChangeParams{At: time.Now(), ID: run.ChangeID, ProjectID: current.ProjectID, OldState: current.State, OldRunID: current.RunID, State: ChangeInProgress.String(), RunID: current.RunID, Branch: current.Branch})); err != nil {
						t.Fatal(err)
					}
				}
			case "suppressed":
				completionExec(t, q.db, `CREATE FUNCTION veto_stamp() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NULL; END $$`)
				completionExec(t, q.db, `CREATE TRIGGER veto_stamp BEFORE UPDATE ON changes FOR EACH ROW EXECUTE FUNCTION veto_stamp()`)
			}
			q.db.Pool().SetMaxOpenConns(1)
			failure := errors.New("later adoption participant failed")
			err := q.db.InTx(context.Background(), func(tx *database.Tx) error {
				revision, err := UpliftAdoptionIn(context.Background(), tx, ref)
				if err != nil {
					return err
				}
				if test == "replay" {
					if revision != 7 {
						t.Errorf("revision %d", revision)
					}
					return nil
				}
				if err := StampUpliftAdoptionIn(context.Background(), tx, UpliftAdoption{Change: ref, Revision: 7}); err != nil {
					return err
				}
				if test == "rollback" {
					return failure
				}
				return nil
			})
			if test == "new" || test == "replay" {
				if err != nil {
					t.Fatal(err)
				}
				wantEqual(t, "adopted revision", storedChange(t, q.db, run.ChangeID).AdoptedRevision, 7)
			} else if test == "rollback" {
				if !errors.Is(err, failure) {
					t.Fatal(err)
				}
				wantEqual(t, "adopted revision", storedChange(t, q.db, run.ChangeID).AdoptedRevision, 0)
			} else if !errors.Is(err, errChangeEditConflict) {
				t.Fatalf("invalid stamp: %v", err)
			}
		})
	}
}

// Projection and participant fixtures supply immutable Change identity at creation.
// They omit Creator's receipts so the operation's own transaction stays observable.
func storedChangeRunFixture(t *testing.T, in workflowsql.InsertChangeParams) (queries, *Run) {
	t.Helper()
	db := testDB(t)
	in.ProjectID = workflowTestProject(t, db, "completion", "Completion", "")
	in.AcceptanceJson = []byte(`{}`)
	in.TitleOwnership = "human"
	in.Title = "A change"
	ch, err := workflowsql.New(db.SQL()).InsertChange(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	run := insertStoredRun(t, db, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: ch.ProjectID, ChangeID: ch.ID, WorkflowName: "test", WorkflowVersion: 1, Kind: "pipeline", Status: RunRunning.String(), CurrentStage: "finish", DefinitionJson: []byte(`{"kind":"pipeline","role":"working","stages":[{"name":"finish"}]}`)})
	return queries{db: db}, run
}
