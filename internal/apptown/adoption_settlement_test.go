package apptown

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/projectlifecycle"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
)

func TestAdoptionRequiresProjectUpdateEvenWhenUnchanged(t *testing.T) {
	t.Parallel()
	for _, unchanged := range []bool{false, true} {
		t.Run(fmt.Sprint(unchanged), func(t *testing.T) {
			a := testApp(t)
			var p *project.Project
			var err error
			before := 0
			if unchanged {
				p, err = a.lifecycle.Create(context.Background(), projectlifecycle.Draft{Slug: "adopting", Name: "Adopting", Actor: "alice"})
				before = 1
			} else {
				id := plainTestProject(t, a.DB, "adopting", "Adopting")
				if err := a.DB.InTx(context.Background(), func(tx *database.Tx) error { return adoption.InitializeIn(t.Context(), tx, id, 0, "") }); err != nil {
					t.Fatal(err)
				}
				var record project.Project
				record, err = project.ByIDIn(context.Background(), a.DB, id)
				p = &record
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := a.Adoption.NoteDrift(t.Context(), p.ID, "needs review"); err != nil {
				t.Fatal(err)
			}
			completionExec(t, a.DB, `CREATE FUNCTION veto_adoption_update() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NULL; END $$`)
			completionExec(t, a.DB, `CREATE TRIGGER veto_adoption_update BEFORE UPDATE ON adoptions FOR EACH ROW EXECUTE FUNCTION veto_adoption_update()`)
			if _, err := a.recordAdoption(p, "edit", "reviewed", "alice"); err == nil {
				t.Fatal("accepted a missing project update")
			}
			requireAdoptionHistory(t, a, p.ID, before)
			requireCurrentAdoption(t, a, p.ID, int64(before), "needs review")
			completionExec(t, a.DB, `DROP TRIGGER veto_adoption_update ON adoptions`)
			ad, err := a.recordAdoption(p, "edit", "reviewed", "alice")
			if err != nil || ad == nil {
				t.Fatalf("retry: %+v %v", ad, err)
			}
			requireAdoptionHistory(t, a, p.ID, 1)
			requireCurrentAdoption(t, a, p.ID, 1, "")
		})
	}
}

func TestWorkflowUpliftAdoptionAndMarkerCommitTogether(t *testing.T) {
	t.Parallel()
	a, ctx, base := changeScenario(t, schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{{Name: "finish"}}})
	uplift, err := a.Work.Create(ctx, workflow.ChangeDraft{ProjectID: base.ProjectID, Type: "uplift", Title: "An uplift"})
	if err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: an uplift admitted at the current adoption revision, as the uplift door records it; that door needs a prepared candidate this settlement test does not exercise, and the landing refuses an uplift worked under another revision.
	completionExec(t, a.DB, `UPDATE changes SET expected_revision=$1 WHERE id=$2`, currentAdoption(a.DB, base.ProjectID).Revision, uplift.ID)
	run, err := a.Work.StartRun(ctx, workflow.StartChangeRun{ChangeID: uplift.ID, Workflow: "test"})
	if err != nil {
		t.Fatal(err)
	}
	a.Work.Wait()
	if out, err := a.Work.Advance(ctx, run.ID); err != nil || !out.Advanced {
		t.Fatalf("complete uplift run: %+v %v", out, err)
	}
	snapshot, err := a.Work.Snapshot(ctx, run.ID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	run = snapshot.Run
	if run.Status != workflow.RunDone {
		t.Fatalf("uplift run did not complete: %s", run.Status)
	}
	completed, err := a.Work.Change(ctx, run.ChangeID)
	if err != nil || completed.State != workflow.ChangeShipped {
		t.Fatalf("uplift change did not ship: %+v %v", completed, err)
	}
	store := a.DB
	p := a.projectByID(ctx, run.ProjectID)
	if p == nil {
		t.Fatal("uplift project disappeared")
	}
	currentRecord := currentAdoption(store, p.ID)
	if currentRecord == nil {
		t.Fatal("uplift project has no current adoption")
	}
	current := currentRecord.Revision
	c := &adoption.Candidate{Receipt: adoption.EmptyReceipt(), ExpectedRevision: current}
	c.Digest = adoption.Digest(c.Receipt)
	dbtest.FailNextStatement(t, a.DB, "UPDATE changes SET adopted_revision")
	if _, err := a.lifecycle.SettleUplift(adoption.WithSystemAuthority(context.Background(), p.ID), p, c, "system", "test", run.ChangeID); !errors.Is(err, database.ErrInjectedFailure) {
		t.Fatalf("adopt: %v", err)
	}
	requireAdoptionHistory(t, a, p.ID, 1)
	if now, err := a.Adoption.Current(ctx, p.ID); err != nil || now.Revision != current {
		t.Fatalf("failed uplift moved the adoption: %+v %v", now, err)
	}
	for i := 0; i < 2; i++ {
		rev, err := a.lifecycle.SettleUplift(adoption.WithSystemAuthority(context.Background(), p.ID), p, c, "system", "test", run.ChangeID)
		if err != nil || rev != current+1 {
			t.Fatalf("adopt %d: %d %v", i, rev, err)
		}
	}
	requireAdoptionHistory(t, a, p.ID, 2)
	if got := ownedChange(t, a, run.ChangeID).AdoptedRevision; got != current+1 {
		t.Fatalf("uplift marker: %d, want %d", got, current+1)
	}
	// A contradictory marker is not healed by guessing from a matching digest.
	// sql-guarantee: Erase only the committed uplift marker while retaining its adoption receipt; supported adoption commits both and cannot produce this contradictory replay state.
	completionExec(t, store, `UPDATE changes SET adopted_revision=0 WHERE id=$1`, run.ChangeID)
	if _, err := a.lifecycle.SettleUplift(adoption.WithSystemAuthority(context.Background(), p.ID), p, c, "system", "recovery", run.ChangeID); err == nil {
		t.Fatal("missing marker was silently reinterpreted")
	}
	requireAdoptionHistory(t, a, p.ID, 2)
	if got := ownedChange(t, a, run.ChangeID).AdoptedRevision; got != 0 {
		t.Fatalf("missing marker was rewritten: %d", got)
	}
}

// requireAdoptionHistory fails unless the project has exactly want recorded
// adoption revisions. The fixtures hold one project, so its history is all of it.
func requireAdoptionHistory(t *testing.T, a *App, projectID ids.ID, want int) {
	t.Helper()
	history, err := a.Adoption.History(t.Context(), projectID, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != want {
		t.Fatalf("%d adoption revisions recorded, want %d", len(history), want)
	}
}

// requireCurrentAdoption fails unless the project's current adoption reads at
// the revision with the drift note.
func requireCurrentAdoption(t *testing.T, a *App, projectID ids.ID, revision int64, drift string) {
	t.Helper()
	current, err := a.Adoption.Current(t.Context(), projectID)
	if err != nil || current == nil || current.Revision != revision || current.Drift != drift {
		t.Fatalf("current adoption: %+v %v, want revision %d drift %q", current, err, revision, drift)
	}
}
