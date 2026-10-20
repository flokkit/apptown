package scm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"flokkit.io/forge/internal/execfile"
	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/gitexec"
	"flokkit.io/forge/internal/gitexec/gittest"
)

type publicationFixture struct {
	t         *testing.T
	db        *database.DB
	publisher *Branches
	projectID ids.ID
	hub       string
}

// publicationHubTemplate is a hub whose main and work are one commit.
var publicationHubTemplate = gittest.NewTemplate(func(t testing.TB, dir string) {
	gittest.Repository(t, dir, "main", gittest.Commit{Message: "base"})
	gittest.Run(t, dir, "branch", "work")
})

func newPublicationFixture(t *testing.T) *publicationFixture {
	t.Helper()
	db := testDB(t)
	projectID, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "publish", Name: "Publish"})
	})
	if err != nil {
		t.Fatal(err)
	}
	hub := publicationHubTemplate.Copy(t, "hub")
	return &publicationFixture{t, db, NewBranches(db, &RepositoryLocks{}), projectID, hub}
}

// writer is a separate clone with the work branch checked out.
func (f *publicationFixture) writer(name string) string {
	f.t.Helper()
	dir := filepath.Join(f.t.TempDir(), name)
	gittest.RunAsTester(f.t, f.t.TempDir(), "clone", "-q", "--no-hardlinks", "-b", "work", f.hub, dir)
	return dir
}

func (f *publicationFixture) publish(from string, grant Grant) (Published, error) {
	return f.publisher.Publish(context.Background(), Publication{ProjectID: f.projectID, Repository: f.hub, Branch: "work", From: from, Grant: grant})
}

func (f *publicationFixture) advance(holder string) Authority {
	f.t.Helper()
	a, err := database.Transact(context.Background(), f.db, func(tx *database.Tx) (Authority, error) {
		return AdvanceAuthorityIn(context.Background(), tx, f.projectID, "work", holder)
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return a
}

func (f *publicationFixture) authority() Authority {
	f.t.Helper()
	a, err := ReadAuthorityIn(context.Background(), f.db, f.projectID, "work")
	if err != nil {
		f.t.Fatal(err)
	}
	return a
}

func (f *publicationFixture) tip() string { return Ref(context.Background(), f.hub, "refs/heads/work") }

func commitIn(t *testing.T, dir, message string) string {
	t.Helper()
	gittest.RunAsTester(t, dir, "commit", "-q", "--allow-empty", "-m", message)
	return gittest.RunAsTester(t, dir, "rev-parse", "HEAD")
}

func TestPublishingNeverOverwritesAnotherWritersCommits(t *testing.T) {
	f := newPublicationFixture(t)
	grant := Grant{Generation: f.authority().Generation}
	first, second := f.writer("first"), f.writer("second")
	published := commitIn(t, first, "first writer")
	if _, err := f.publish(first, grant); err != nil {
		t.Fatal(err)
	}
	commitIn(t, second, "second writer, unaware of the first")
	_, err := f.publish(second, grant)
	var moved *BranchMoved
	if !errors.As(err, &moved) || !moved.NotForward {
		t.Fatalf("publishing a commit that does not build on the branch = %v, want a not-forward refusal", err)
	}
	if f.tip() != published {
		t.Fatalf("branch = %s, want the first writer's %s kept", f.tip(), published)
	}
}

func TestAHandoverRefusesTheEarlierHoldersFastForward(t *testing.T) {
	f := newPublicationFixture(t)
	run := f.advance("run:1")
	before := f.tip()
	stale := f.writer("stale")
	commitIn(t, stale, "late result of the replaced run")
	// The takeover publishes nothing: only ownership changes.
	f.advance("chat:1")
	if _, err := f.publish(stale, Grant{Generation: run.Generation}); !errors.Is(err, ErrAuthoritySuperseded) {
		t.Fatalf("stale fast-forward after a handover = %v, want %v", err, ErrAuthoritySuperseded)
	}
	if f.tip() != before {
		t.Fatalf("branch moved to %s after a refused publication", f.tip())
	}
}

func TestRewindingDoesNotReviveAReplacedAttempt(t *testing.T) {
	f := newPublicationFixture(t)
	base := f.tip()
	attemptA := f.advance("run:1")
	a := f.writer("a")
	commitIn(t, a, "attempt A, delayed")
	b := f.writer("b")
	commitIn(t, b, "attempt B's first commit")
	attemptB := f.advance("run:1") // same run, replaced attempt
	if _, err := f.publish(b, Grant{Generation: attemptB.Generation}); err != nil {
		t.Fatal(err)
	}
	gittest.RunAsTester(t, b, "reset", "-q", "--hard", base)
	if _, err := f.publisher.Publish(context.Background(), Publication{ProjectID: f.projectID, Repository: f.hub, Branch: "work", From: b, Rewind: true, Grant: Grant{Generation: attemptB.Generation}}); err != nil {
		t.Fatalf("restart rewinding to its base: %v", err)
	}
	if f.tip() != base {
		t.Fatalf("branch = %s after the rewind, want base %s", f.tip(), base)
	}
	if _, err := f.publish(a, Grant{Generation: attemptA.Generation}); !errors.Is(err, ErrAuthoritySuperseded) {
		t.Fatalf("attempt A's delayed publication from the same base = %v, want %v", err, ErrAuthoritySuperseded)
	}
}

func TestARewindIsNeverSideways(t *testing.T) {
	f := newPublicationFixture(t)
	grant := Grant{Generation: f.authority().Generation}
	one, other := f.writer("one"), f.writer("other")
	kept := commitIn(t, one, "published")
	if _, err := f.publish(one, grant); err != nil {
		t.Fatal(err)
	}
	commitIn(t, other, "unrelated")
	_, err := f.publisher.Publish(context.Background(), Publication{ProjectID: f.projectID, Repository: f.hub, Branch: "work", From: other, Rewind: true, Grant: grant})
	var moved *BranchMoved
	if !errors.As(err, &moved) || f.tip() != kept {
		t.Fatalf("sideways move allowed as a rewind: %v, branch %s", err, f.tip())
	}
}

func TestAPublisherThatExpectedAnotherTipIsRefused(t *testing.T) {
	f := newPublicationFixture(t)
	grant := Grant{Generation: f.authority().Generation}
	started := f.tip()
	moved := f.writer("mover")
	commitIn(t, moved, "someone else")
	if _, err := f.publish(moved, grant); err != nil {
		t.Fatal(err)
	}
	late := f.writer("late")
	commitIn(t, late, "builds on the new tip, but started from the old one")
	_, err := f.publisher.Publish(context.Background(), Publication{ProjectID: f.projectID, Repository: f.hub, Branch: "work", From: late, Expected: started, Grant: grant})
	var refused *BranchMoved
	if !errors.As(err, &refused) || refused.Expected != started {
		t.Fatalf("publishing from an outdated expected tip = %v", err)
	}
}

func TestActingForTheHolderRequiresTheCurrentHolder(t *testing.T) {
	f := newPublicationFixture(t)
	f.advance("ticket:7")
	w := f.writer("w")
	commitIn(t, w, "desktop push")
	if _, err := f.publish(w, Grant{Holder: "ticket:6"}); !errors.Is(err, ErrAuthoritySuperseded) {
		t.Fatalf("an earlier ticket = %v, want %v", err, ErrAuthoritySuperseded)
	}
	if _, err := f.publish(w, Grant{Holder: "ticket:7"}); err != nil {
		t.Fatalf("the current ticket: %v", err)
	}
}

// A git process can outlive a crashed AppTown: here it holds a prepared
// transaction whose commit has not been processed yet. The next AppTown must
// not treat the unchanged branch as abandoned while that git may still act.
func TestAHandoverWaitsForAGitProcessThatOutlivedTheForge(t *testing.T) {
	f := newPublicationFixture(t)
	gen := f.authority().Generation
	w := f.writer("w")
	proposed := commitIn(t, w, "in flight when AppTown died")
	gittest.RunAsTester(t, f.hub, "fetch", "-q", w, "work:refs/apptown/incoming/test")
	expected := f.tip()
	if _, err := f.publisher.begin(context.Background(), Publication{ProjectID: f.projectID, Repository: f.hub, Branch: "work", Grant: Grant{Generation: gen}}, expected, proposed); err != nil {
		t.Fatal(err)
	}
	survivor, err := gitexec.PrepareUpdate(context.Background(), f.hub, "refs/heads/work", proposed, expected)
	if err != nil {
		t.Fatal(err)
	}
	report, err := f.publisher.Recover(context.Background())
	if err != nil || report.Unknown != 1 {
		t.Fatalf("recovery while git holds the branch = %+v, %v; want it unknown", report, err)
	}
	if _, err := database.Transact(context.Background(), f.db, func(tx *database.Tx) (Authority, error) {
		return AdvanceAuthorityIn(context.Background(), tx, f.projectID, "work", "chat:1")
	}); !errors.Is(err, ErrPublicationUnsettled) {
		t.Fatalf("handover while the old git may still move the branch = %v, want %v", err, ErrPublicationUnsettled)
	}
	if err := survivor.Commit(); err != nil {
		t.Fatal(err)
	}
	report, err = f.publisher.Recover(context.Background())
	if err != nil || report.Settled != 1 || f.tip() != proposed {
		t.Fatalf("recovery after the old git finished = %+v, %v, branch %s", report, err, f.tip())
	}
	f.advance("chat:1")
}

func TestACrashAfterGitMovedTheBranchSettlesWithoutReplay(t *testing.T) {
	f := newPublicationFixture(t)
	gen := f.authority().Generation
	w := f.writer("w")
	proposed := commitIn(t, w, "published just before the crash")
	gittest.RunAsTester(t, f.hub, "fetch", "-q", w, "work:refs/apptown/incoming/test")
	expected := f.tip()
	if _, err := f.publisher.begin(context.Background(), Publication{ProjectID: f.projectID, Repository: f.hub, Branch: "work", Grant: Grant{Generation: gen}}, expected, proposed); err != nil {
		t.Fatal(err)
	}
	gittest.RunAsTester(t, f.hub, "update-ref", "refs/heads/work", proposed, expected)
	report, err := f.publisher.Recover(context.Background())
	if err != nil || report.Settled != 1 || report.Unknown != 0 {
		t.Fatalf("recovery = %+v, %v; want the publication settled", report, err)
	}
	if f.tip() != proposed {
		t.Fatalf("branch = %s, want %s", f.tip(), proposed)
	}
	f.advance("chat:1")
}

func TestACrashBeforeGitCommittedLeavesTheBranchAndSettles(t *testing.T) {
	f := newPublicationFixture(t)
	gen := f.authority().Generation
	w := f.writer("w")
	proposed := commitIn(t, w, "never committed")
	expected := f.tip()
	if _, err := f.publisher.begin(context.Background(), Publication{ProjectID: f.projectID, Repository: f.hub, Branch: "work", Grant: Grant{Generation: gen}}, expected, proposed); err != nil {
		t.Fatal(err)
	}
	report, err := f.publisher.Recover(context.Background())
	if err != nil || report.Settled != 1 || f.tip() != expected {
		t.Fatalf("recovery = %+v, %v, branch %s; want settled with the branch unchanged", report, err, f.tip())
	}
}

func TestAnUnsettledPublicationHoldsTheNextOne(t *testing.T) {
	f := newPublicationFixture(t)
	gen := f.authority().Generation
	w := f.writer("w")
	proposed := commitIn(t, w, "held")
	gittest.RunAsTester(t, f.hub, "fetch", "-q", w, "work:refs/apptown/incoming/test")
	if _, err := f.publisher.begin(context.Background(), Publication{ProjectID: f.projectID, Repository: f.hub, Branch: "work", Grant: Grant{Generation: gen}}, f.tip(), proposed); err != nil {
		t.Fatal(err)
	}
	held, err := gitexec.PrepareUpdate(context.Background(), f.hub, "refs/heads/work", proposed, f.tip())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Commit() }()
	next := f.writer("next")
	commitIn(t, next, "waits")
	if _, err := f.publish(next, Grant{Generation: gen}); !errors.Is(err, ErrPublicationUnsettled) {
		t.Fatalf("publication while another is unsettled = %v, want %v", err, ErrPublicationUnsettled)
	}
}

// The first explicit handover must revoke grants captured while authority was implicit.
func TestTheFirstHandoverRevokesTheInitialGeneration(t *testing.T) {
	f := newPublicationFixture(t)
	before := f.authority()
	stale := f.writer("stale-initial")
	commitIn(t, stale, "work admitted before the first handover")
	after := f.advance("ticket:1")
	_, err := f.publish(stale, Grant{Generation: before.Generation})
	if !errors.Is(err, ErrAuthoritySuperseded) {
		t.Fatalf("initial grant %d still publishes after handover to %+v: %v", before.Generation, after, err)
	}
}

// A missing authority row must not leave first publication and handover unserialized.
func TestTheFirstPublicationAndTheFirstHandoverAreSerialized(t *testing.T) {
	f := newPublicationFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	initial := f.authority()
	w := f.writer("first")
	proposed := commitIn(t, w, "first publication")
	expected := f.tip()
	// sql-guarantee: suspend the real begin operation after its authority read,
	// before its publication INSERT becomes visible to a concurrent handover.
	completionExec(t, f.db, `CREATE FUNCTION hold_first_publication() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(8675309); RETURN NEW; END $$`)
	completionExec(t, f.db, `CREATE TRIGGER hold_first_publication BEFORE INSERT ON branch_publications FOR EACH ROW EXECUTE FUNCTION hold_first_publication()`)
	gate, err := f.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Rollback()
	// sql-guarantee: takes the advisory lock the injected publication trigger waits on; a held lock, not a read
	if _, err := gate.SQL().ExecContext(ctx, `SELECT pg_advisory_xact_lock(8675309)`); err != nil {
		t.Fatal(err)
	}
	begun := make(chan error, 1)
	go func() {
		_, err := f.publisher.begin(ctx, Publication{ProjectID: f.projectID, Repository: f.hub, Branch: "work", Grant: Grant{Generation: initial.Generation}}, expected, proposed)
		begun <- err
	}()
	for {
		// The barrier is Postgres's own lock table: no owner operation can say
		// that a transaction is waiting at the injected trigger.
		var waiting bool
		// sql-guarantee: only Postgres's lock table can say a transaction is waiting at the injected barrier; no owner read can
		if err := f.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND objid=8675309 AND NOT granted)`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("publication did not reach barrier")
		case <-time.After(10 * time.Millisecond):
		}
	}
	handed := make(chan error, 1)
	go func() {
		_, err := database.Transact(ctx, f.db, func(tx *database.Tx) (Authority, error) {
			return AdvanceAuthorityIn(ctx, tx, f.projectID, "work", "ticket:2")
		})
		handed <- err
	}()
	select {
	case err := <-handed:
		if err == nil {
			_ = gate.Rollback()
			if err := <-begun; err != nil {
				t.Fatal(err)
			}
			t.Fatal("first handover committed while first publication still admitted the earlier generation")
		}
	case <-time.After(100 * time.Millisecond):
		// Correct serialization may block the handover until begin has finished.
		_ = gate.Rollback()
		<-begun
		<-handed
	}
}

// A publication of a commit already in the hub (a restart's rewind to its
// base) is a compare-and-swap of the ref there: it fetches from no writer,
// refuses a commit the hub does not have, and runs no hook of the hub's,
// since Git in the hub runs hardened.
func TestPublishingACommitMovesTheRefInTheHubAlone(t *testing.T) {
	f := newPublicationFixture(t)
	base := f.tip()
	grant := Grant{Generation: f.authority().Generation}
	w := f.writer("w")
	commitIn(t, w, "published work")
	if _, err := f.publish(w, grant); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(t.TempDir(), "fired")
	hook := filepath.Join(f.hub, ".git", "hooks", "reference-transaction")
	if err := execfile.Write(hook, []byte("#!/bin/sh\ntouch "+sentinel+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	absent := "0123456789abcdef0123456789abcdef01234567"
	if _, err := f.publisher.Publish(context.Background(), Publication{ProjectID: f.projectID, Repository: f.hub, Branch: "work", Commit: absent, Rewind: true, Grant: grant}); err == nil {
		t.Fatal("a commit the hub does not have was published")
	}
	if _, err := f.publisher.Publish(context.Background(), Publication{ProjectID: f.projectID, Repository: f.hub, Branch: "work", Commit: base, Rewind: true, Grant: grant}); err != nil {
		t.Fatalf("rewinding to the base: %v", err)
	}
	if f.tip() != base {
		t.Fatalf("branch = %s after the rewind, want base %s", f.tip(), base)
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("a hook in the hub ran: %v", err)
	}
}

// Publish says what happened: a refusal the owner decided (Refused) moved
// nothing; a publication whose branch moved and whose record could not be
// settled is applied (ErrPublicationApplied), not refused, and is settled
// by the owner afterwards (Settle).
func TestAPublicationsOutcomeIsClassified(t *testing.T) {
	f := newPublicationFixture(t)
	grant := Grant{Generation: f.authority().Generation}
	w, late := f.writer("w"), f.writer("late")
	proposed := commitIn(t, w, "applied, not settled")
	restore := f.db.FailNextStatement("-- name: SettlePublication")
	defer restore()
	got, err := f.publish(w, grant)
	if !errors.Is(err, ErrPublicationApplied) || Refused(err) || got.Tip != proposed || !got.Changed || f.tip() != proposed {
		t.Fatalf("a publication applied and not settled = %+v, %v (refused %v); branch %s", got, err, Refused(err), f.tip())
	}
	if err := f.publisher.Settle(context.Background(), f.projectID, "work"); err != nil {
		t.Fatal(err)
	}
	if report, err := f.publisher.Recover(context.Background()); err != nil || report.Inspected != 0 {
		t.Fatalf("the applied publication was not settled: %+v %v", report, err)
	}
	commitIn(t, late, "does not build on the applied one")
	if _, err := f.publish(late, grant); !Refused(err) {
		t.Fatalf("a not-forward publication = %v, want a refusal", err)
	}
	f.advance("chat:1")
	if _, err := f.publish(w, grant); !Refused(err) {
		t.Fatalf("a superseded publication = %v, want a refusal", err)
	}
	for _, err := range []error{ErrPublicationUnknown, ErrPublicationUnsettled, errors.New("the database is gone")} {
		if Refused(err) {
			t.Fatalf("%v is taken as a refusal", err)
		}
	}
	if !Refused(fmt.Errorf("checking: %w", ErrNotPublished)) {
		t.Fatal("a refusal of new work is not a refusal")
	}
}

// Recovery reads the branch hardened and error-aware: a branch it cannot
// read leaves its publication unknown, never taken as a first publication
// that did not happen. Read again once readable, it settles.
func TestAnUnreadableBranchLeavesItsPublicationUnknown(t *testing.T) {
	f := newPublicationFixture(t)
	w := f.writer("w")
	gittest.RunAsTester(t, w, "checkout", "-q", "-b", "fresh")
	proposed := commitIn(t, w, "the branch's first publication")
	t.Cleanup(f.db.FailNextStatement("-- name: SettlePublication"))
	if _, err := f.publisher.Publish(context.Background(), Publication{ProjectID: f.projectID, Repository: f.hub, Branch: "fresh", From: w,
		Grant: Grant{Generation: 1}}); !errors.Is(err, ErrPublicationApplied) {
		t.Fatalf("the settlement fault was not exercised: %v", err)
	}
	ref := filepath.Join(f.hub, ".git", "refs", "heads", "fresh")
	if err := os.WriteFile(ref, []byte("not an object\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	report, err := f.publisher.Recover(context.Background())
	if err != nil || report.Unknown != 1 || report.Settled != 0 {
		t.Fatalf("recovery of an unreadable branch = %+v %v; want it unknown", report, err)
	}
	if err := os.WriteFile(ref, []byte(proposed+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	report, err = f.publisher.Recover(context.Background())
	if err != nil || report.Settled != 1 {
		t.Fatalf("recovery of the readable branch = %+v %v; want it settled", report, err)
	}
}

// A publication whose repository is gone cannot be settled, and does not
// stop startup's recovery: it is recorded unknown and holds its branch.
func TestAPublicationWhoseRepositoryIsGoneStaysUnknown(t *testing.T) {
	f := newPublicationFixture(t)
	gen := f.authority().Generation
	w := f.writer("w")
	proposed := commitIn(t, w, "in flight when the repository went")
	expected := f.tip()
	if _, err := f.publisher.begin(context.Background(), Publication{ProjectID: f.projectID, Repository: f.hub, Branch: "work", Grant: Grant{Generation: gen}}, expected, proposed); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(f.hub); err != nil {
		t.Fatal(err)
	}
	report, err := f.publisher.Recover(context.Background())
	if err != nil || report.Unknown != 1 {
		t.Fatalf("recovery with the repository gone = %+v, %v; want it unknown", report, err)
	}
	if _, err := database.Transact(context.Background(), f.db, func(tx *database.Tx) (Authority, error) {
		return AdvanceAuthorityIn(context.Background(), tx, f.projectID, "work", "chat:1")
	}); !errors.Is(err, ErrPublicationUnsettled) {
		t.Fatalf("handover past an unknown publication = %v, want %v", err, ErrPublicationUnsettled)
	}
}
