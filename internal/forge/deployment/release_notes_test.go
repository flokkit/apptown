package deployment

import (
	"context"
	"testing"
	"time"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/forge/deployment/internal/releasesql"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/ids"
)

func releaseNotesFixture(t *testing.T) (*database.DB, ids.ID, ids.ID) {
	t.Helper()
	db := testDB(t)
	change, err := database.Transact(context.Background(), db, func(tx *database.Tx) (workflow.CreatedChange, error) {
		pid, err := project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "notes"})
		if err != nil {
			return workflow.CreatedChange{}, err
		}
		ch, err := workflow.CreateChangeIn(context.Background(), tx, workflow.ChangeDraft{ProjectID: pid, Title: "A change"}, "test", time.Now())
		if err == nil {
			_, err = workflow.TransitionChangeIn(context.Background(), tx, pid, ch.Change.ID, workflow.ChangeMove{Action: workflow.ShipChange}, time.Now())
		}
		return ch, err
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewReleases(db).Start(context.Background(), ReleaseDraft{Source: ReleaseFromBranch, ProjectID: change.Change.ProjectID, Deployment: "main", Actor: "test"}, "release shipped work")
	if err != nil {
		t.Fatal(err)
	}
	return db, change.Change.ProjectID, change.Change.ID
}

func TestNarrativeReleaseNotesSelectLinkedEmptyNotes(t *testing.T) {
	t.Parallel()
	db, pid, change := releaseNotesFixture(t)
	// A later change was not carried by the already-created release.
	later, err := workflow.NewCreator(db, nil).Create(context.Background(), workflow.ChangeDraft{ProjectID: pid, Title: "Later"}, "test")
	if err != nil {
		t.Fatal(err)
	}
	other, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "other"})
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		project, change ids.ID
		note, want      string
	}{
		{pid, later.ID, "unlinked", ""}, {other, change, "foreign", ""}, {pid, change, "human release notes", "human release notes"}, {pid, change, "model overwrite", "human release notes"},
	} {
		if err := db.InTx(context.Background(), func(tx *database.Tx) error {
			if _, err := releasesql.New(tx.SQL()).LockReleaseProject(context.Background(), tc.project); err != nil {
				return err
			}
			return FillChangeReleaseNotesIn(context.Background(), tx, tc.project, tc.change, tc.note)
		}); err != nil {
			t.Fatal(err)
		}
		release, err := NewReleases(db).ByNumber(context.Background(), pid, 1)
		if err != nil || release.Notes != tc.want {
			t.Fatalf("release notes: %+v %v; want %q", release, err, tc.want)
		}
	}
}

func TestNarrativeReleaseNoteFailuresAreReported(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"write", "suppressed", "deferred"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			db, pid, change := releaseNotesFixture(t)
			if mode == "write" {
				dbtest.FailNextStatement(t, db, "-- name: FillChangeReleaseNotes")
			} else {
				body, trigger := "RETURN NULL;", "CREATE TRIGGER veto_notes BEFORE UPDATE ON releases FOR EACH ROW EXECUTE FUNCTION veto_notes()"
				if mode == "deferred" {
					body = "RAISE EXCEPTION 'notes rejected at commit';"
					trigger = "CREATE CONSTRAINT TRIGGER veto_notes AFTER UPDATE ON releases DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION veto_notes()"
				}
				mustExec(t, db, `CREATE FUNCTION veto_notes() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN `+body+` END $$`)
				mustExec(t, db, trigger)
			}
			if err := db.InTx(context.Background(), func(tx *database.Tx) error {
				if _, err := releasesql.New(tx.SQL()).LockReleaseProject(context.Background(), pid); err != nil {
					return err
				}
				return FillChangeReleaseNotesIn(context.Background(), tx, pid, change, "uncommitted")
			}); err == nil {
				t.Fatal("release note failure ignored")
			}
			release, err := NewReleases(db).ByNumber(context.Background(), pid, 1)
			if err != nil || release.Notes != "" {
				t.Fatalf("partial notes: %+v %v", release, err)
			}
		})
	}
}
