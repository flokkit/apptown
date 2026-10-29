package deployment

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
)

func releaseCreationFixture(t *testing.T, source ReleaseSource) (*database.DB, *Releases, ReleaseDraft) {
	t.Helper()
	db := testDB(t)
	p := createTestProject(t, db, "release-create", "Release create")
	depID := storedDeploymentFixture(t, db, deploymentsql.EnsureStandingParams{ID: ids.New(time.Now()), ProjectID: p, Name: "main"})
	bundle := storedBundleFixture(t, db, deploymentsql.InsertBundleParams{ID: ids.New(time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)), ProjectID: p, ContractRevision: "selected", CreatedAt: time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)})
	return db, NewReleases(db), ReleaseDraft{Source: source, ProjectID: p, DeploymentID: depID,
		BundleID: bundle, Deployment: "main", Revision: "selected", Actor: "alice", Notes: "https://release.example"}
}

func TestReleaseCreationMembership(t *testing.T) {
	t.Parallel()
	for _, source := range []ReleaseSource{ReleaseFromBundle, ReleaseFromBranch} {
		t.Run(string(source), func(t *testing.T) {
			db, store, draft := releaseCreationFixture(t, source)
			// sql-guarantee: TestReleaseCreationMembership needs one historical row numbered seven to distinguish MAX(number)+1 from COUNT(*)+1; CreateRelease only allocates numbers, and its January 2 date is the branch cutoff.
			mustInsert(t, db, `INSERT INTO releases(id,project_id,number,state,deployment,created_at) VALUES ($1,$2,7,'released','main','2026-01-02')`, draft.ProjectID)
			var changes []ids.ID
			for _, day := range []int{1, 3, 4} {
				// Straddling the January 2 release and the January 3 bundle.
				changes = append(changes, shippedChangeAt(t, db, draft.ProjectID, time.Date(2026, 1, day, 0, 0, 0, 0, time.UTC)))
			}
			// sql-guarantee: TestReleaseCreationMembership gives an open Change a matching closed_at to test the shipped-state predicate independently; Workflow transitions cannot create that contradiction and its queries are internal.
			open := mustInsert(t, db, `INSERT INTO changes(id,number,project_id,title,type,state,closed_at,created_at) VALUES ($1,$2,$3,'Not shipped','feature','in_progress','2026-01-03','2026-10-09T00:00:00Z')`, fixtureNumber(), draft.ProjectID)
			r, err := store.Start(context.Background(), draft, "started by alice")
			if err != nil || r.Number != 8 {
				t.Fatalf("admission: %+v %v", r, err)
			}
			want := changes[:2]
			if source == ReleaseFromBranch {
				want = changes[1:]
			}
			// These are every change on the instance, so the ones the release
			// carries are its whole membership.
			var carried []ids.ID
			for _, c := range append(changes, open) {
				for _, rel := range changeReleases(t, db, draft.ProjectID, c) {
					if rel.ID == r.ID {
						carried = append(carried, c)
					}
				}
			}
			if fmt.Sprint(carried) != fmt.Sprint(want) {
				t.Fatalf("release carries %v, want %v", carried, want)
			}
			if rel := storedRelease(t, db, draft.ProjectID, r.ID); rel.State != Releasing || rel.BundleID != draft.BundleID || rel.CommitSHA != "selected" || rel.SourceSHA != "selected" || rel.ApprovedBy != "alice" {
				t.Fatalf("admitted release: %+v", rel)
			}
			started := 0
			for _, e := range releaseEvents(t, db, r.ID) {
				if e.Text == "release #8 started by alice" {
					started++
				}
			}
			wantCount(t, "start events", started, 1)
		})
	}
}

// steppingClock is an AppTown clock an hour ahead of the real one that moves
// a second at every read, so each stamp is strictly after the last.
func steppingClock() func() time.Time {
	var n atomic.Int64
	base := time.Now().Add(time.Hour)
	return func() time.Time { return base.Add(time.Duration(n.Add(1)) * time.Second) }
}

// A release carries the Changes that closed before it, judged by the
// AppTown's clock on both sides: when the Change closed (Work's clock) and
// when the bundle was cut or the last release made (deployment's clock).
// AppTown's clock here runs an hour ahead of the real one and the
// database's.
//
// Kills: a bundle cut, or a workflow release made, at the real clock (the
// Change closed before it reads an hour after it); a Change's close stamped
// by the database (the Change closed after the bundle reads an hour before
// it).
func TestAReleaseCarriesTheChangesClosedBeforeItByAppTownsClock(t *testing.T) {
	t.Parallel()
	t.Run("bundle", func(t *testing.T) {
		t.Parallel()
		db, store, draft := releaseCreationFixture(t, ReleaseFromBundle)
		now := steppingClock()
		store.clock = now
		bundles := NewBundles(db)
		bundles.clock = now
		before := shippedChangeAt(t, db, draft.ProjectID, now())
		bundle, err := bundles.Cut(t.Context(), draft.ProjectID, "selected", nil, nil, nil, BundleOrigin{})
		if err != nil {
			t.Fatal(err)
		}
		after := shippedChangeAt(t, db, draft.ProjectID, now())
		draft.BundleID = bundle
		r, err := store.Start(context.Background(), draft, "started by alice")
		if err != nil {
			t.Fatal(err)
		}
		wantCarried(t, db, draft.ProjectID, r.ID, map[ids.ID]bool{before: true, after: false})
	})
	t.Run("branch", func(t *testing.T) {
		t.Parallel()
		db, store, draft := releaseCreationFixture(t, ReleaseFromBranch)
		now := steppingClock()
		store.clock = now
		shipped := shippedChangeAt(t, db, draft.ProjectID, now())
		if err := db.InTx(context.Background(), func(tx *database.Tx) error {
			if err := lockReleaseProjectIn(tx, draft.ProjectID); err != nil {
				return err
			}
			_, err := CreateReleaseIn(context.Background(), tx, ReleaseDraft{At: now(), Source: ReleaseFromWorkflow, ProjectID: draft.ProjectID, ChangeID: shipped, Deployment: "main"})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		after := shippedChangeAt(t, db, draft.ProjectID, now())
		r, err := store.Start(context.Background(), draft, "started by alice")
		if err != nil {
			t.Fatal(err)
		}
		wantCarried(t, db, draft.ProjectID, r.ID, map[ids.ID]bool{shipped: false, after: true})
	})
}

func wantCarried(t *testing.T, db *database.DB, projectID, releaseID ids.ID, want map[ids.ID]bool) {
	t.Helper()
	for change, carried := range want {
		got := false
		for _, rel := range changeReleases(t, db, projectID, change) {
			got = got || rel.ID == releaseID
		}
		if got != carried {
			t.Errorf("change %d carried by release %d: %v, want %v", change, releaseID, got, carried)
		}
	}
}

func TestReleaseCreationWaitsForCurrentCutoff(t *testing.T) {
	t.Parallel()
	db, store, draft := releaseCreationFixture(t, ReleaseFromBranch)
	// Closed exactly at the January 3 release cutoff (excluded) and after it.
	older := shippedChangeAt(t, db, draft.ProjectID, time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC))
	newer := shippedChangeAt(t, db, draft.ProjectID, time.Date(2026, 1, 4, 0, 0, 0, 0, time.UTC))
	tx, err := db.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockReleaseProjectIn(tx, draft.ProjectID); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { _, err := store.Start(context.Background(), draft, "waiting release"); result <- err }()
	dbtest.WaitForLock(t, db, "")
	// sql-guarantee: TestReleaseCreationWaitsForCurrentCutoff commits one sparse number-seven row and the new cutoff under this transaction lock; CreateRelease allocates one, while seven sequential rows would mask COUNT-based numbering.
	if _, err := tx.SQL().ExecContext(context.Background(), `INSERT INTO releases(id,project_id,number,state,deployment,created_at) VALUES ($1,$2,7,'released','main','2026-01-03')`, ids.New(time.Now()), draft.ProjectID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	eighth := 0
	for _, r := range everyRelease(t, db) {
		if r.Number == 8 && r.State == Releasing {
			eighth++
		}
	}
	wantCount(t, "releasing release #8", eighth, 1)
	wantN(t, "releases carrying the earlier change", changeReleases(t, db, draft.ProjectID, older), 0)
	wantN(t, "releases carrying the newer change", changeReleases(t, db, draft.ProjectID, newer), 1)
}

func TestReleaseCreationAtomicAdmission(t *testing.T) {
	t.Parallel()
	for _, target := range []string{"releases", "release_changes", "release_events"} {
		t.Run(target, func(t *testing.T) {
			db, store, draft := releaseCreationFixture(t, ReleaseFromBundle)
			shippedChangeFixture(t, db, draft.ProjectID)
			draft.BundleID = cutBundleFixture(t, db, draft.ProjectID, "selected", BundleOrigin{})
			mustExec(t, db, `CREATE FUNCTION veto_release_record() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NULL; END $$`)
			mustExec(t, db, `CREATE TRIGGER veto_release_record BEFORE INSERT ON `+target+` FOR EACH ROW EXECUTE FUNCTION veto_release_record()`)
			if _, err := store.Start(context.Background(), draft, "must be recorded"); err == nil {
				t.Fatal("admission succeeded without its required record")
			}
			// Release changes and events reference their release, so no
			// release on the instance means neither was left behind.
			wantN(t, "releases", everyRelease(t, db), 0)
			mustExec(t, db, `DROP TRIGGER veto_release_record ON `+target)
			if r, err := store.Start(context.Background(), draft, "try again"); err != nil || r.Number != 1 {
				t.Fatalf("retry after rollback: %+v %v", r, err)
			}
		})
	}
}

func TestReleaseCreationConcurrencyAndScope(t *testing.T) {
	t.Parallel()
	db, store, draft := releaseCreationFixture(t, ReleaseFromBundle)
	results := race(8, func(int) int {
		if _, err := store.Start(context.Background(), draft, "one winner"); err != nil {
			if !errors.Is(err, ErrReleaseInFlight) {
				t.Error(err)
			}
			return 0
		}
		return 1
	})
	if count(results, 1) != 1 {
		t.Fatalf("admissions: %v", results)
	}
	other := draft
	other.DeploymentID = storedDeploymentFixture(t, db, deploymentsql.EnsureStandingParams{ID: ids.New(time.Now()), ProjectID: draft.ProjectID, Name: "other"})
	other.Deployment = "other"
	if r, err := store.Start(context.Background(), other, "another deployment"); err != nil || r.Number != 2 {
		t.Fatalf("independent deployment: %+v %v", r, err)
	}
	other.Source = ReleaseFromBranch
	if _, err := store.Start(context.Background(), other, "project-wide admission"); !errors.Is(err, ErrReleaseInFlight) {
		t.Fatalf("intranet admission ignored another release: %v", err)
	}
	wantN(t, "releases", everyRelease(t, db), 2)
}
