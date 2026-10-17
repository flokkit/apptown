package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
	"flokkit.io/forge/internal/ids"
)

// moveDatabaseClock moves this test database's now() by an interval, so a
// test can show which clock a stored time came from.
func moveDatabaseClock(t *testing.T, db *database.DB, by string) {
	t.Helper()
	for _, statement := range []string{
		`CREATE OR REPLACE FUNCTION public.now() RETURNS timestamptz LANGUAGE plpgsql STABLE AS $$ BEGIN RETURN pg_catalog.now() + interval '` + by + `'; END $$`,
		`DO $$ BEGIN EXECUTE format('ALTER DATABASE %I SET search_path = public, pg_catalog', current_database()); END $$`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	db.Pool().SetMaxIdleConns(0)
	if _, err := db.Exec(`DO $$ BEGIN IF now() = pg_catalog.now() THEN RAISE EXCEPTION 'the database''s clock was not moved'; END IF; END $$`); err != nil {
		t.Fatal(err)
	}
}

// An approval brief kept for a step is stale once any step finishes after
// it. Both times are the forge's: the brief's making and the step's finish
// are written from the owner's clock, so the database's clock, an hour
// either way, changes nothing.
//
// Kills: the step's finish stamped by the database's now() (an hour behind,
// it reads as finishing before the brief, which is shown as current); the
// brief stamped by the database (an hour ahead, it reads as newer than the
// step).
func TestAKeptBriefIsStaleOnceAStepFinishesAfterItWhateverTheDatabasesClock(t *testing.T) {
	t.Parallel()
	for _, skew := range []string{"-1 hour", "1 hour"} {
		t.Run(skew, func(t *testing.T) {
			t.Parallel()
			q, run, approval := activityFixture(t)
			ctx := context.Background()
			moveDatabaseClock(t, q.db, skew)
			if _, err := AddArtifactIn(ctx, q.db, run.ID, "approval-brief", fmt.Sprintf("step-%d", approval), "", "kept", time.Now()); err != nil {
				t.Fatal(err)
			}
			later := insertStoredStep(t, q.db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "finish", Name: "build", Type: "build", Status: StatusPending.String(), ConfigJson: json.RawMessage(`{}`)})
			snapshot, err := readStep(ctx, q.db, later)
			if err != nil {
				t.Fatal(err)
			}
			owner := newStepAttempts(q.db, func(kind string) bool { return kind == "build" })
			claim, err := owner.Start(ctx, snapshot, "build")
			if err != nil {
				t.Fatal(err)
			}
			if err := owner.Finish(ctx, claim, StepOutcome{Status: StatusPass}); err != nil {
				t.Fatal(err)
			}
			brief, err := newService(q.db, allowProject(run.ProjectID), Runtime{}).ApprovalBrief(ctx, approval)
			if err != nil {
				t.Fatal(err)
			}
			if brief.Cached != "" {
				t.Fatal("a brief kept before a step finished is shown as current")
			}
		})
	}
}

// A gate Work creates (here the current-rules gate a policy refusal opens)
// is a step finishing like any other: a brief kept before it is stale, and
// one kept after it is current, whatever the database's clock says. Its
// time is the forge's.
//
// Kills: a created gate stamped by the database (an hour behind, it reads
// as finishing before the brief kept before it; an hour ahead, as
// finishing after the brief kept after it).
func TestABriefIsJudgedAgainstAGateWorkCreatesByTheForgesClock(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		skew        string
		briefFirst  bool
		wantCurrent bool
	}{{"-1 hour", true, false}, {"1 hour", false, true}} {
		t.Run(tc.skew, func(t *testing.T) {
			t.Parallel()
			db, run := policyGateFixture(t)
			ctx := context.Background()
			approval := insertStoredStep(t, db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: run.CurrentStage, Name: "accept", Type: "approval", Status: StatusPending.String(), ConfigJson: json.RawMessage(`{}`)})
			moveDatabaseClock(t, db, tc.skew)
			keep := func() {
				if _, err := AddArtifactIn(ctx, db, run.ID, "approval-brief", fmt.Sprintf("step-%d", approval), "", "kept", time.Now()); err != nil {
					t.Fatal(err)
				}
			}
			if tc.briefFirst {
				keep()
			}
			if err := db.InTx(ctx, func(tx *database.Tx) error {
				gate, err := (lawStore{db: db, rules: policyLawRules{}}).evaluateIn(ctx, tx, run, false)
				if err == nil && (gate == nil || gate.Type != "policy/current") {
					return fmt.Errorf("no current-rules gate was opened: %+v", gate)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if !tc.briefFirst {
				keep()
			}
			brief, err := newService(db, allowProject(run.ProjectID), Runtime{}).ApprovalBrief(ctx, approval)
			if err != nil {
				t.Fatal(err)
			}
			if current := brief.Cached != ""; current != tc.wantCurrent {
				t.Fatalf("the kept brief is current: %v, want %v", current, tc.wantCurrent)
			}
		})
	}
}
