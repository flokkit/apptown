package workflow

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/database"

	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
)

// A root run on a change holds the change's branch; each attempt captures the
// authority it was admitted under, and a replaced attempt's is revoked.
func TestAReplacedAttemptLosesTheBranch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := testDB(t)
	projectID := changeTestProject(t, db, "branches")
	change, err := NewCreator(db, nil).Create(ctx, ChangeDraft{ProjectID: projectID, Title: "held"}, "creator")
	if err != nil {
		t.Fatal(err)
	}
	// Repository-backed from here on; creating a Change there would need a
	// workspace, which this owner test does not prepare.
	if err := project.New(db, nil).SetDefaultBranch(ctx, projectID, "main"); err != nil {
		t.Fatal(err)
	}
	admissions := newRunAdmissions(db, NewStages(nil, func(kind string) bool { return kind == "script" }), nil)
	run, err := admissions.Create(ctx, StartRequest{ProjectID: projectID, ChangeID: change.ID, WorkflowName: "test", InputsJSON: "{}", RequestedBy: "operator"},
		PreparedRun{Workflow: DefinitionRef{Name: "test", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "finish", Steps: []schema.Step{{Name: "work", Uses: "script"}}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := admissions.Prepared(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	source, err := ReadSourceIn(ctx, db, RunRef{ID: run.ID, ProjectID: projectID})
	if err != nil {
		t.Fatal(err)
	}
	held, err := scm.ReadAuthorityIn(ctx, db, projectID, source.Branch)
	if err != nil || held.Holder != scm.RunHolder(run.ID) {
		t.Fatalf("starting the run did not hand it the branch: %+v %v", held, err)
	}
	q := queries{db: db}
	id := admittedStepID(t, q, run, "work")
	attempts := newStepAttempts(db, func(kind string) bool { return kind == "script" })
	start := func() *Step {
		t.Helper()
		snapshot, err := readStep(ctx, db, id)
		if err != nil {
			t.Fatal(err)
		}
		claim, err := attempts.Start(ctx, snapshot, "script")
		if err != nil {
			t.Fatal(err)
		}
		return claim
	}
	first := start()
	if first.SourceGeneration() != held.Generation {
		t.Fatalf("the first attempt captured %d, want the run's %d", first.SourceGeneration(), held.Generation)
	}
	if n, err := workflowsql.New(db.SQL()).RetryStep(ctx, workflowsql.RetryStepParams{ConfigJson: first.Config, ID: id, Attempt: first.Attempt}); err != nil || n != 1 {
		t.Fatalf("resetting the step for a retry: %d %v", n, err)
	}
	second := start()
	if second.SourceGeneration() <= first.SourceGeneration() {
		t.Fatalf("the retry captured %d, not beyond the replaced attempt's %d", second.SourceGeneration(), first.SourceGeneration())
	}
	current, err := scm.ReadAuthorityIn(ctx, db, projectID, source.Branch)
	if err != nil || current.Generation != second.SourceGeneration() || current.Holder != scm.RunHolder(run.ID) {
		t.Fatalf("after the retry the branch is %+v, want the retry's generation held by the run", current)
	}
	if reread, _ := readStep(ctx, db, id); reread.SourceGeneration() != 0 {
		t.Fatal("reading a step granted it an authority")
	}
}

func TestAStepIsNotAdmittedAfterItsRunLostTheBranch(t *testing.T) {
	for _, retry := range []bool{false, true} {
		t.Run(map[bool]string{false: "new-step", true: "retry"}[retry], func(t *testing.T) {
			ctx := context.Background()
			db := testDB(t)
			projectID := changeTestProject(t, db, "claim-holder")
			change, err := NewCreator(db, nil).Create(ctx, ChangeDraft{ProjectID: projectID, Title: "ownership"}, "creator")
			if err != nil {
				t.Fatal(err)
			}
			if err := project.New(db, nil).SetDefaultBranch(ctx, projectID, "main"); err != nil {
				t.Fatal(err)
			}
			admissions := newRunAdmissions(db, NewStages(nil, func(kind string) bool { return kind == "script" }), nil)
			run, err := admissions.Create(ctx, StartRequest{ProjectID: projectID, ChangeID: change.ID, WorkflowName: "test", InputsJSON: "{}", RequestedBy: "operator"}, PreparedRun{Workflow: DefinitionRef{Name: "test", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "work", Steps: []schema.Step{{Name: "work", Uses: "script"}}}}}})
			if err != nil {
				t.Fatal(err)
			}
			if err := admissions.Prepared(ctx, run.ID); err != nil {
				t.Fatal(err)
			}
			source, err := ReadSourceIn(ctx, db, RunRef{ID: run.ID, ProjectID: projectID})
			if err != nil {
				t.Fatal(err)
			}
			id := admittedStepID(t, queries{db: db}, run, "work")
			attempts := newStepAttempts(db, func(kind string) bool { return kind == "script" })
			if retry {
				snapshot, err := readStep(ctx, db, id)
				if err != nil {
					t.Fatal(err)
				}
				claim, err := attempts.Start(ctx, snapshot, "script")
				if err != nil {
					t.Fatal(err)
				}
				// sql-guarantee: reproduce the existing source-authority test's pending retry.
				if _, err := workflowsql.New(db.SQL()).RetryStep(ctx, workflowsql.RetryStepParams{ID: id, Attempt: claim.Attempt, ConfigJson: claim.Config}); err != nil {
					t.Fatal(err)
				}
			}
			held, err := database.Transact(ctx, db, func(tx *database.Tx) (scm.Authority, error) {
				return scm.AdvanceAuthorityIn(ctx, tx, projectID, source.Branch, "ticket:42")
			})
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := readStep(ctx, db, id)
			if err != nil {
				t.Fatal(err)
			}
			claim, err := attempts.Start(ctx, snapshot, "script")
			current, readErr := scm.ReadAuthorityIn(ctx, db, projectID, source.Branch)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if err == nil {
				t.Fatalf("run %d claimed generation %d after handover to %+v; authority now %+v", run.ID, claim.SourceGeneration(), held, current)
			}
		})
	}
}

// Sending work back starts a new root run, which takes the change like any
// writing run: refused while a chat holds it, holding it otherwise.
func TestASentBackContinuationTakesTheChange(t *testing.T) {
	t.Parallel()
	for _, chatHolds := range []bool{false, true} {
		t.Run(map[bool]string{false: "free", true: "chat holds"}[chatHolds], func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			db := testDB(t)
			projectID := changeTestProject(t, db, "sendback")
			change, err := NewCreator(db, nil).Create(ctx, ChangeDraft{ProjectID: projectID, Title: "sent back"}, "creator")
			if err != nil {
				t.Fatal(err)
			}
			if err := project.New(db, nil).SetDefaultBranch(ctx, projectID, "main"); err != nil {
				t.Fatal(err)
			}
			automatic := []string{"agent/pi"}
			admissions := newRunAdmissions(db, NewStages(nil, func(kind string) bool { return kind == "agent/pi" }), nil)
			run, err := admissions.Create(ctx, StartRequest{ProjectID: projectID, ChangeID: change.ID, WorkflowName: "implement", InputsJSON: "{}", RequestedBy: "operator"},
				PreparedRun{Workflow: DefinitionRef{Name: "implement", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "work", OnEnter: []schema.Step{{Name: "build", Uses: "agent/pi"}}, Steps: []schema.Step{{Name: "accept", Uses: "approval"}}}}}})
			if err != nil {
				t.Fatal(err)
			}
			if err := admissions.Prepared(ctx, run.ID); err != nil {
				t.Fatal(err)
			}
			// A completed producer, without running a model.
			transitionScopeRun(t, db, workflowsql.TransitionRunParams{At: time.Now(), ID: run.ID, NextStatus: "done"})
			recordScopeRunSteps(t, db, run.ID, "pass")
			source, err := ReadSourceIn(ctx, db, RunRef{ID: run.ID, ProjectID: projectID})
			if err != nil {
				t.Fatal(err)
			}
			if chatHolds {
				// The finished run's hold has ended, so a chat takes the change.
				if err := db.InTx(ctx, func(tx *database.Tx) error {
					_, err := TakeChangeIn(ctx, tx, projectID, source.Branch, scm.ChatHolder("chat-1"), nil, time.Now())
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			s := sendBackStore{db: db, automatic: automatic, abandonments: NewAbandonments(db, abandonRecorderFunc(noAbandonEffects), &scm.RepositoryLocks{})}
			result, err := s.sendBack(ctx, change.ID, "fix this", "operator", "", projectID)
			held, readErr := scm.ReadAuthorityIn(ctx, db, projectID, source.Branch)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if chatHolds {
				var refused *ChangeHeld
				if !errors.As(err, &refused) || held.Holder != scm.ChatHolder("chat-1") {
					t.Fatalf("send-back admitted run %d beside the chat: %v, holder %s", result.RunID, err, held.Holder)
				}
				return
			}
			if err != nil || held.Holder != scm.RunHolder(result.RunID) {
				t.Fatalf("send-back did not take its holder: result=%+v err=%v holder=%s", result, err, held.Holder)
			}
		})
	}
}
