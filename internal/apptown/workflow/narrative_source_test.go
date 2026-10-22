package workflow

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
)

func narrativeRun(t *testing.T, db *database.DB, ch *Change, name string, steps ...schema.Step) *Run {
	t.Helper()
	owner := newRunAdmissions(db, NewStages(nil, func(string) bool { return false }), nil)
	run, err := owner.Create(context.Background(), StartRequest{ProjectID: ch.ProjectID, ChangeID: ch.ID, WorkflowName: name, InputsJSON: "{}", RequestedBy: "test"}, PreparedRun{Workflow: DefinitionRef{Name: name, Version: 1}, Spec: schema.Spec{Kind: "pipeline", Stages: []schema.Stage{{Name: "work", Steps: steps}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.Prepared(context.Background(), run.ID); err != nil {
		t.Fatal(err)
	}
	return run
}

func TestNarrativePublicationRefusesChangedSource(t *testing.T) {
	t.Parallel()
	for _, changed := range []string{"body", "state", "run", "branch", "base", "repository"} {
		t.Run(changed, func(t *testing.T) {
			t.Parallel()
			owner, source := narrativeFixture(t)
			ch := source.Change
			ctx := context.Background()
			var err error
			// Repository configuration and branch/base preparation are separate
			// supported acts; observe between them to test actual stale prose.
			if changed == "branch" || changed == "base" {
				if err := project.New(owner.db, nil).SetDefaultBranch(ctx, ch.ProjectID, "main"); err != nil {
					t.Fatal(err)
				}
				if changed == "base" {
					if _, err := (changeStore{owner.db, clocked{}}).start(ctx, ch.ProjectID, ch.ID, "test"); err != nil {
						t.Fatal(err)
					}
				}
				source, err = owner.Source(ctx, ch.ID)
				if err != nil {
					t.Fatal(err)
				}
			}
			switch changed {
			case "body":
				body := "new brief"
				_, err = editChangeAs(ctx, owner.db, "alice", ChangeEdit{ID: ch.ID, Body: &body, ExpectedRevision: revision(0)})
			case "state":
				_, err = NewAbandonments(owner.db, abandonRecorderFunc(noAbandonEffects), &scm.RepositoryLocks{}).Abandon(ctx, Abandonment{ChangeID: ch.ID, Who: "alice", Reason: "no longer needed"})
			case "run":
				narrativeRun(t, owner.db, ch, "new work")
			case "branch":
				_, err = (changeStore{owner.db, clocked{}}).start(ctx, ch.ProjectID, ch.ID, "test")
			case "base":
				var before changeWorkspaceSource
				before, err = readChangeWorkspace(ctx, owner.db, ChangeRef{ID: ch.ID, ProjectID: ch.ProjectID})
				if err == nil {
					err = owner.db.InTx(ctx, func(tx *database.Tx) error {
						if _, err := workflowsql.New(tx.SQL()).ProjectForUpdate(ctx, ch.ProjectID); err != nil {
							return err
						}
						return recordChangeWorkspaceIn(ctx, tx, before, "observed-head")
					})
				}
			case "repository":
				err = project.New(owner.db, nil).SetDefaultBranch(ctx, ch.ProjectID, "main")
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := owner.Publish(ctx, NarrativePublication{Source: source, Draft: NarrativeDraft{Title: "new", Note: "new note"}}); !errors.Is(err, ErrNarrativeChanged) {
				t.Fatalf("stale %s: %v", changed, err)
			}
			assertNarrative(t, owner, ch.ID, "automatic", "previous")
			changeReceipts(t, owner.db, "change.narrative-updated", 1)
		})
	}
}

func TestNarrativePublicationCancellationReleasesLockWait(t *testing.T) {
	t.Parallel()
	owner, source := narrativeFixture(t)
	gate, err := owner.db.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Rollback()
	if _, err := workflowsql.New(gate.SQL()).ProjectForUpdate(context.Background(), source.Change.ProjectID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- owner.Publish(ctx, NarrativePublication{Source: source, Draft: NarrativeDraft{Note: "new note"}})
	}()
	dbtest.WaitForLock(t, owner.db, "-- name: ProjectForUpdate")
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled publication: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("publication retained cancelled lock wait")
	}
	assertNarrative(t, owner, source.Change.ID, "automatic", "previous")
	changeReceipts(t, owner.db, "change.narrative-updated", 1)
}

func TestNarrativeSourceOwnsSelectionOrderingAndOneSnapshot(t *testing.T) {
	t.Parallel()
	owner, source := narrativeFixture(t)
	db, ch := owner.db, source.Change
	first := narrativeRun(t, db, ch, "earlier")
	if err := (advanceStore{db: db, completion: completionRecorderFunc(noCompletionEffects)}).complete(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	second := narrativeRun(t, db, ch, "later", schema.Step{Name: "child", Uses: "workflow/call"})
	steps, err := readSteps(context.Background(), db, second.ID)
	if err != nil || len(steps) != 1 {
		t.Fatalf("call step: %+v %v", steps, err)
	}
	admissions := newRunAdmissions(db, NewStages(nil, func(string) bool { return false }), nil)
	child, err := admissions.Create(context.Background(), StartRequest{ProjectID: ch.ProjectID, ChangeID: ch.ID, WorkflowName: "child", InputsJSON: "{}", RequestedBy: "test", ParentRunID: second.ID, ParentStepID: steps[0].ID, ParentStepAttempt: steps[0].Attempt}, PreparedRun{Workflow: DefinitionRef{Name: "child", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Stages: []schema.Stage{{Name: "finish"}}}})
	if err != nil {
		t.Fatal(err)
	}
	other := changeTestProject(t, db, "other")
	foreign, err := NewCreator(db, nil).Create(context.Background(), ChangeDraft{ProjectID: other, Title: "Other change"}, "test")
	if err != nil {
		t.Fatal(err)
	}
	foreignRun := narrativeRun(t, db, foreign, "foreign")
	for _, artifact := range []struct {
		run        ids.ID
		kind, body string
	}{
		{first.ID, "report", "older"}, {second.ID, "plan", "before plan"}, {child.ID, "build-report", "before report"}, {second.ID, "scan-report", "excluded scan"}, {foreignRun.ID, "report", "excluded foreign"},
	} {
		if _, err := AddArtifactIn(context.Background(), db, artifact.run, artifact.kind, "test", "", artifact.body, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	gate, err := db.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Rollback()
	// This is a storage snapshot test: hold its final read while a valid edit
	// and a new evidence record commit together in the owning transaction.
	if _, err := gate.Exec(`LOCK TABLE artifacts IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	type result struct {
		source NarrativeSource
		err    error
	}
	done := make(chan result, 1)
	go func() { got, err := owner.Source(ctx, ch.ID); done <- result{got, err} }()
	dbtest.WaitForLock(t, db, "-- name: NarrativeArtifacts")
	q := workflowsql.New(gate.SQL())
	if _, err := q.ProjectForUpdate(ctx, ch.ProjectID); err != nil {
		t.Fatal(err)
	}
	if n, err := q.EditChangeDescription(ctx, workflowsql.EditChangeDescriptionParams{ID: ch.ID, ProjectID: ch.ProjectID, Body: sql.NullString{String: "after", Valid: true}}); err != nil || n != 1 {
		t.Fatalf("edit: %d %v", n, err)
	}
	if _, err := AddArtifactIn(ctx, gate, child.ID, "report", "later", "", "after report", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := gate.Commit(); err != nil {
		t.Fatal(err)
	}
	got := <-done
	if got.err != nil {
		t.Fatal(got.err)
	}
	old := got.source
	if old.Change.Body != "brief" || len(old.Runs) != 2 || old.Runs[0].ID != first.ID || old.Runs[1].ID != second.ID || old.Runs[0].State != RunDone || old.Runs[1].State != RunRunning || len(old.Artifacts) != 2 || old.Artifacts[0].Body != "before report" || old.Artifacts[1].Body != "before plan" {
		t.Fatalf("snapshot selection: %+v %+v", old, old.Change)
	}
	fresh, err := owner.Source(ctx, ch.ID)
	if err != nil || fresh.Change.Body != "after" || len(fresh.Artifacts) != 2 || fresh.Artifacts[0].Body != "after report" {
		t.Fatalf("fresh source: %+v %v", fresh, err)
	}
}
