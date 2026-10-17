package workflow

import (
	"context"
	"errors"
	"testing"
	"time"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/ids"
)

type noTarget struct{}

func (noTarget) RecordTargetIn(context.Context, *database.Tx, TargetChange) (RecordedTarget, error) {
	return RecordedTarget{}, errors.New("no target in this test")
}

// Every Work door that creates or restarts a project's work asks the
// project's lifecycle right after its lock, before its own checks: a run
// that is live in a project being deleted (marked between its admission
// and the door) still takes no new step, decision, retry, send-back,
// landing or release.
//
// Kills: removing the lifecycle check from any one door.
func TestEveryWorkDoorAsksTheProjectsLifecycle(t *testing.T) {
	t.Parallel()
	for _, state := range []string{project.Deleting, project.Deleted} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			q, run, step := admitSingleStep(t, schema.Step{Name: "gate", Uses: "approval"})
			db := q.db
			snapshot, err := ReadStepIn(ctx, db, step)
			if err != nil {
				t.Fatal(err)
			}
			decision, err := stepDecisionStore{db, clocked{}}.prepare(ctx, step)
			if err != nil {
				t.Fatal(err)
			}
			if err := db.InTx(ctx, func(tx *database.Tx) error {
				if err := project.MarkDeletingIn(ctx, tx, run.ProjectID, access.Actor{Name: "boss"}, ids.Nil, false, time.Now()); err != nil {
					return err
				}
				if state == project.Deleted {
					return project.MarkDeletedIn(ctx, tx, run.ProjectID, time.Now(), time.Now().Add(time.Hour))
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			inTx := func(f func(*database.Tx) error) error { return db.InTx(ctx, f) }
			doors := map[string]func() error{
				"change creation": func() error {
					_, err := NewCreator(db, nil).Create(ctx, ChangeDraft{ProjectID: run.ProjectID, Title: "more"}, "boss")
					return err
				},
				"change start": func() error {
					_, err := (changeStore{db, clocked{}}).start(ctx, run.ProjectID, run.ChangeID, "boss")
					return err
				},
				"run admission": func() error {
					_, err := newRunAdmissions(db, NewStages(nil, nil), nil).Create(ctx, StartRequest{ProjectID: run.ProjectID, ChangeID: run.ChangeID, WorkflowName: "test", InputsJSON: "{}", RequestedBy: "boss"},
						PreparedRun{Workflow: DefinitionRef{Name: "test", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "s", Steps: []schema.Step{{Name: "gate", Uses: "approval"}}}}}})
					return err
				},
				"send-back": func() error {
					_, err := (sendBackStore{db: db, abandonments: NewAbandonments(db, abandonRecorderFunc(noAbandonEffects), &scm.RepositoryLocks{})}).sendBack(ctx, run.ChangeID, "again", "boss", "", ids.Nil)
					return err
				},
				"branch import": func() error {
					_, err := (changeStore{db, clocked{}}).importBranch(ctx, ImportBranch{ProjectID: run.ProjectID, Branch: "feature"}, importSource{}, "boss")
					return err
				},
				"uplift": func() error {
					_, err := (changeStore{db, clocked{}}).openUplift(ctx, Uplift{ProjectID: run.ProjectID, Acceptance: Acceptance{TargetDigest: "sha256:x"}, Words: []string{"w"}, Target: noTarget{}}, "boss")
					return err
				},
				"landing and release (the run snapshot lock)": func() error {
					return inTx(func(tx *database.Tx) error { _, err := lockRunSnapshotIn(ctx, tx, run); return err })
				},
				"decision": func() error {
					return inTx(func(tx *database.Tx) error { _, _, err := decision.lock(ctx, tx); return err })
				},
				"retry": func() error {
					_, err := (stepRetries{db: db}).retryExpected(ctx, retryIntent{RetryCommand: RetryCommand{StepID: step}, RunID: run.ID}, nil)
					return err
				},
				"step start": func() error {
					_, err := newStepAttempts(db, func(string) bool { return true }).Start(ctx, snapshot, "")
					return err
				},
			}
			for name, door := range doors {
				if err := door(); !errors.Is(err, project.ErrNotActive) {
					t.Errorf("%s in a project %s: %v", name, state, err)
				}
			}
		})
	}
}
