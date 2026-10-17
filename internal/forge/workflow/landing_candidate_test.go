package workflow

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
)

// A new candidate is new evidence: gates that ran on the old one, passed or
// failed, run again. Pinning the same candidate resets nothing, and a merge
// refuses while any passed gate names another commit.
func TestANewLandingCandidateRerunsItsSourceGates(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, change := changeEditFixture(t, ChangeDraft{Title: "A landing"})
	every := func(string) bool { return true }
	admissions := newRunAdmissions(db, NewStages(nil, every), nil)
	run, err := admissions.Create(ctx, StartRequest{ProjectID: change.ProjectID, ChangeID: change.ID, WorkflowName: "land", InputsJSON: "{}", RequestedBy: "operator"},
		PreparedRun{Workflow: DefinitionRef{Name: "land", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{{Name: "gates", Steps: []schema.Step{
			{Name: "catch-up", Uses: "reconcile"}, {Name: "tests", Uses: "tests"}, {Name: "scan-source", Uses: "scan"},
		}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := admissions.Prepared(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	attempts := newStepAttempts(db, every)
	start := func(name string) *Step {
		t.Helper()
		snapshot, err := readStep(ctx, db, admittedStepID(t, queries{db: db}, run, name))
		if err != nil {
			t.Fatal(err)
		}
		claim, err := attempts.Start(ctx, snapshot, snapshot.Type)
		if err != nil {
			t.Fatal(err)
		}
		return claim
	}
	finish := func(claim *Step, status StepState, candidate string) {
		t.Helper()
		evidence, _ := json.Marshal(map[string]string{"candidate": candidate})
		if err := attempts.Finish(ctx, claim, StepOutcome{Status: status, Evidence: evidence}); err != nil {
			t.Fatal(err)
		}
	}
	x, y, main := "1111111111111111111111111111111111111111", "2222222222222222222222222222222222222222", "3333333333333333333333333333333333333333"
	pin := func(candidate string) []string {
		t.Helper()
		claim := start("catch-up")
		e := &Execution{service: &Service{db: db}, ctx: ctx, step: claim, run: run, claim: claim}
		reset, err := e.PinLandingCandidate(LandingCandidate{Candidate: candidate, Target: main})
		if err != nil {
			t.Fatal(err)
		}
		finish(claim, StatusPass, candidate)
		return reset
	}
	if reset := pin(x); len(reset) != 0 {
		t.Fatalf("the first candidate reset %v", reset)
	}
	finish(start("tests"), StatusPass, x)
	finish(start("scan-source"), StatusFail, x)
	if refusal, err := LandingCandidateRefusal(ctx, db, run.ID, LandingCandidate{Candidate: y}); err != nil || refusal == "" {
		t.Fatalf("a gate that passed on another commit did not refuse the merge: %q %v", refusal, err)
	}
	if n, err := invalidateAndRetryForTest(ctx, db, run, "catch-up"); err != nil || n != 1 {
		t.Fatalf("resetting the catch-up for a recheck: %d %v", n, err)
	}
	reset := pin(y)
	if len(reset) != 2 {
		t.Fatalf("a new candidate reset %v, want both gates that ran on the old one", reset)
	}
	for _, name := range []string{"tests", "scan-source"} {
		step, err := readStep(ctx, db, admittedStepID(t, queries{db: db}, run, name))
		if err != nil || step.Status != StatusPending {
			t.Fatalf("%s after a new candidate: %+v %v", name, step, err)
		}
	}
	finish(start("tests"), StatusPass, y)
	finish(start("scan-source"), StatusPass, y)
	if refusal, err := LandingCandidateRefusal(ctx, db, run.ID, LandingCandidate{Candidate: y}); err != nil || refusal != "" {
		t.Fatalf("gates that all checked the candidate refused the merge: %q %v", refusal, err)
	}
	if _, err := invalidateAndRetryForTest(ctx, db, run, "catch-up"); err != nil {
		t.Fatal(err)
	}
	if reset := pin(y); len(reset) != 0 {
		t.Fatalf("repinning the same candidate reset %v", reset)
	}
}

// invalidateAndRetryForTest puts one finished step back to pending through the
// owner's recheck transition, as a landing recheck does.
func invalidateAndRetryForTest(ctx context.Context, db *database.DB, run *Run, name string) (int, error) {
	return database.Transact(ctx, db, func(tx *database.Tx) (int, error) {
		steps, err := stepRecords(workflowsql.New(tx.SQL()).StageStepsForUpdate(ctx, workflowsql.StageStepsForUpdateParams{RunID: run.ID, Stage: run.CurrentStage}))
		if err != nil {
			return 0, err
		}
		for _, step := range steps {
			if step.Name == name {
				return 1, recheckStepIn(ctx, tx, step)
			}
		}
		return 0, nil
	})
}

// An acceptance given on the working run counts for the landing only while
// the landing merges exactly the work as accepted.
func TestAcceptanceCarriesOnlyForTheAcceptedWork(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, tc := range []struct {
		name      string
		accepted  string
		admission LandingCandidate
		later     *LandingCandidate
		carries   bool
	}{
		{"merged as accepted", "a1", LandingCandidate{Candidate: "a1", Target: "m1", By: "admission"}, &LandingCandidate{Candidate: "a1", Target: "m1", By: "catch-up"}, true},
		{"published after the acceptance", "a0", LandingCandidate{Candidate: "a1", Target: "m1", By: "admission"}, nil, false},
		{"an acceptance that named no commit", "", LandingCandidate{Candidate: "a1", Target: "m1", By: "admission"}, nil, false},
		{"caught up with main", "a1", LandingCandidate{Candidate: "a1", Target: "m1", By: "admission"}, &LandingCandidate{Candidate: "c2", Target: "m2", By: "catch-up"}, false},
		{"swept at admission", "a1", LandingCandidate{Candidate: "a1", Target: "m1", By: "admission", Swept: true}, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q, run := admitWorkflowSpec(t, schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{{Name: "finish"}}})
			record := func(c LandingCandidate) {
				t.Helper()
				if _, err := database.Transact(ctx, q.db, func(tx *database.Tx) (struct{}, error) {
					return struct{}{}, recordReceiptIn(ctx, tx, run.ID, landingCandidateEvent, c, time.Now())
				}); err != nil {
					t.Fatal(err)
				}
			}
			record(tc.admission)
			if tc.later != nil {
				record(*tc.later)
			}
			refusal, err := carryRefusalIn(ctx, q.db, run.ID, tc.accepted)
			if err != nil || (refusal == "") != tc.carries {
				t.Fatalf("carries=%v (%q), want %v: %v", refusal == "", refusal, tc.carries, err)
			}
		})
	}
}

// A merge rests only on results bound to the candidate: a passed check that
// cannot be bound refuses it, and so does an approval the run left behind
// undecided.
func TestAMergeRefusesUnboundChecksAndApprovalsLeftBehind(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	every := func(string) bool { return true }
	pinned := LandingCandidate{Candidate: "c1", Target: "m1"}
	t.Run("an agent review", func(t *testing.T) {
		q, run := admitWorkflowSpec(t, schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{{Name: "gates", Steps: []schema.Step{{Name: "review", Uses: "agent/review"}}}}})
		attempts := newStepAttempts(q.db, every)
		snapshot, err := readStep(ctx, q.db, admittedStepID(t, q, run, "review"))
		if err != nil {
			t.Fatal(err)
		}
		claim, err := attempts.Start(ctx, snapshot, snapshot.Type)
		if err != nil {
			t.Fatal(err)
		}
		if err := attempts.Finish(ctx, claim, StepOutcome{Status: StatusPass, Evidence: json.RawMessage(`{}`)}); err != nil {
			t.Fatal(err)
		}
		if refusal, err := LandingCandidateRefusal(ctx, q.db, run.ID, pinned); err != nil || refusal == "" {
			t.Fatalf("a passed agent review did not refuse the merge: %q %v", refusal, err)
		}
	})
	t.Run("an approval left behind", func(t *testing.T) {
		q, run := admitWorkflowSpec(t, schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{
			{Name: "approve", Steps: []schema.Step{{Name: "ok", Uses: "approval", With: map[string]any{"means": "fine"}}}},
			{Name: "ship"},
		}})
		// sql-guarantee: place the run past a stage whose approval a new candidate reset, a state only a custom layout reaches.
		if _, err := q.db.Exec(`UPDATE runs SET current_stage='ship' WHERE id=?`, run.ID); err != nil {
			t.Fatal(err)
		}
		if refusal, err := LandingCandidateRefusal(ctx, q.db, run.ID, pinned); err != nil || refusal == "" {
			t.Fatalf("an approval left undecided behind the run did not refuse the merge: %q %v", refusal, err)
		}
	})
}
