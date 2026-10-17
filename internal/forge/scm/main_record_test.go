package scm

import (
	"context"
	"errors"
	"math/rand"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/gitexec/gittest"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/strs"
)

// mainHubTemplate is a hub with one commit on main.
var mainHubTemplate = gittest.NewTemplate(func(t testing.TB, dir string) {
	gittest.Repository(t, dir, "main", gittest.Commit{Message: "first"})
})

// newMainHub is the test's own copy of mainHubTemplate.
func newMainHub(t *testing.T) string {
	t.Helper()
	return mainHubTemplate.Copy(t, "hub")
}

func mainProject(t *testing.T) (*database.DB, ids.ID, string) {
	t.Helper()
	db := testDB(t)
	id, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "main", Name: "Main"})
	})
	if err != nil {
		t.Fatal(err)
	}
	return db, id, newMainHub(t)
}

func recordedMain(t *testing.T, db *database.DB, projectID ids.ID) string {
	t.Helper()
	m, known, err := ReadMain(context.Background(), db, projectID)
	if err != nil {
		t.Fatal(err)
	}
	if !known {
		return ""
	}
	return m.SHA
}

func record(t *testing.T, db *database.DB, hub string, projectID ids.ID, via MainVia) {
	t.Helper()
	if err := RecordMain(context.Background(), db, hub, projectID, "main", via, time.Now()); err != nil {
		t.Fatal(err)
	}
}

// rewriteMain replaces main with an unrelated commit, as only an edit outside
// the forge can.
func rewriteMain(t *testing.T, hub string) string {
	t.Helper()
	gittest.RunAsTester(t, hub, "checkout", "-q", "--orphan", "rewritten")
	gittest.RunAsTester(t, hub, "commit", "-q", "--allow-empty", "-m", "rewritten")
	sha := gittest.Run(t, hub, "rev-parse", "HEAD")
	gittest.Run(t, hub, "update-ref", "refs/heads/main", sha)
	gittest.Run(t, hub, "checkout", "-q", "main")
	return sha
}

// The record only goes forward: a first record, a fast-forward, and nothing
// for a main that does not descend from the record or cannot be read.
func TestRecordedMainOnlyGoesForward(t *testing.T) {
	t.Parallel()
	db, id, hub := mainProject(t)
	record(t, db, hub, id, ViaInit)
	first := Ref(t.Context(), hub, "refs/heads/main")
	if got := recordedMain(t, db, id); got != first {
		t.Fatalf("first record %q, want %q", got, first)
	}
	second := commitIn(t, hub, "second")
	record(t, db, hub, id, ViaGenesis)
	if m, _, _ := ReadMain(context.Background(), db, id); m.SHA != second || m.Via != string(ViaGenesis) {
		t.Fatalf("forward record %+v, want %s by genesis", m, second)
	}
	rewriteMain(t, hub)
	record(t, db, hub, id, ViaIntegrityAdopt)
	if got := recordedMain(t, db, id); got != second {
		t.Fatalf("a rewritten main was recorded: %q, want %q kept", got, second)
	}
	gittest.Run(t, hub, "checkout", "-q", "--detach")
	gittest.Run(t, hub, "update-ref", "-d", "refs/heads/main")
	record(t, db, hub, id, ViaGenesis)
	if got := recordedMain(t, db, id); got != second {
		t.Fatalf("an unreadable main changed the record to %q", got)
	}
}

// A recorded main's time is the one its recorder gave, the forge's, for the
// first record and a move forward alike; the database's clock stamps none.
//
// Kills: the record or the move stamped by the database's now().
func TestRecordedMainIsStampedByTheRecordersClock(t *testing.T) {
	t.Parallel()
	db, id, hub := mainProject(t)
	ctx := context.Background()
	first := time.Date(2031, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := RecordMain(ctx, db, hub, id, "main", ViaInit, first); err != nil {
		t.Fatal(err)
	}
	if m, _, err := ReadMain(ctx, db, id); err != nil || !m.RecordedAt.Equal(first) {
		t.Fatalf("first record at %s, want %s: %v", m.RecordedAt, first, err)
	}
	commitIn(t, hub, "second")
	moved := first.Add(time.Hour)
	if err := RecordMain(ctx, db, hub, id, "main", ViaGenesis, moved); err != nil {
		t.Fatal(err)
	}
	if m, _, err := ReadMain(ctx, db, id); err != nil || !m.RecordedAt.Equal(moved) {
		t.Fatalf("moved record at %s, want %s: %v", m.RecordedAt, moved, err)
	}
}

// Two moves prepared from the same record: the second loses the compare and
// set, and the caller's whole transaction rolls back with it.
func TestRecordedMainLosesARacedCompareAndSet(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, id, hub := mainProject(t)
	for _, previous := range []string{"unknown", "known"} {
		if previous == "known" {
			commitIn(t, hub, "known")
			record(t, db, hub, id, ViaGenesis)
		}
		commitIn(t, hub, "moved")
		a, err := PrepareMainMove(ctx, db, hub, id, "main")
		if err != nil {
			t.Fatal(err)
		}
		b, err := PrepareMainMove(ctx, db, hub, id, "main")
		if err != nil {
			t.Fatal(err)
		}
		if err := db.InTx(ctx, func(tx *database.Tx) error { return a.RecordIn(ctx, tx, ViaGenesis, ids.Nil, time.Now()) }); err != nil {
			t.Fatal(err)
		}
		err = db.InTx(ctx, func(tx *database.Tx) error {
			// sql-guarantee: a stand-in for the caller's own settlement write in the same transaction.
			if _, err := tx.SQL().ExecContext(ctx, `UPDATE projects SET blurb='settled' WHERE id=$1`, id); err != nil {
				return err
			}
			return b.RecordIn(ctx, tx, ViaGenesis, ids.Nil, time.Now())
		})
		if !errors.Is(err, ErrMainChanged) {
			t.Fatalf("%s: raced record = %v, want ErrMainChanged", previous, err)
		}
		if p, err := project.ByIDIn(ctx, db, id); err != nil || p.Blurb == "settled" {
			t.Fatalf("%s: the caller's write survived a lost compare-and-set: %+v %v", previous, p, err)
		}
	}
}

// Whatever mix of forge moves and forward commits outside the forge, the
// record is always main or an ancestor of it. A step is a commit outside the
// forge (main moves past the record) or a forge move that records main. The
// seeded walk is twelve steps, "fooofffffoff": every pair of step kinds
// follows the other, and three outside commits run together, so the record
// is seen behind by more than one commit and caught up again. The test fails
// if a changed seed or length stops covering every pair.
func TestRecordedMainIsAlwaysAnAncestorOfMain(t *testing.T) {
	t.Parallel()
	db, id, hub := mainProject(t)
	random := rand.New(rand.NewSource(7))
	followed := map[string]bool{}
	previous := ""
	for step := range 12 {
		kind := "forge"
		if random.Intn(3) == 0 {
			kind = "outside"
		}
		if previous != "" {
			followed[previous+" then "+kind] = true
		}
		previous = kind
		switch kind {
		case "outside":
			commitIn(t, hub, "outside the forge")
		default:
			commitIn(t, hub, "a forge move")
			record(t, db, hub, id, ViaGenesis)
		}
		recorded := recordedMain(t, db, id)
		if recorded == "" {
			continue
		}
		if !ancestor(t.Context(), hub, recorded, "refs/heads/main") {
			t.Fatalf("step %d: recorded %s is not an ancestor of main", step, recorded)
		}
	}
	if recordedMain(t, db, id) == "" {
		t.Fatal("the sequence never recorded main")
	}
	for _, pair := range []string{"forge then forge", "forge then outside", "outside then outside", "outside then forge"} {
		if !followed[pair] {
			t.Fatalf("the walk never had %s; choose a seed or length that does", pair)
		}
	}
}

// The note says what differs, never who moved main.
func TestMainStandingNotes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, id, hub := mainProject(t)
	read := func() MainStanding {
		t.Helper()
		s, err := ReadMainStanding(ctx, db, hub, id, "main")
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	if s := read(); s.State != "unknown" || s.Note != "the forge has no record of where main stands" {
		t.Fatalf("unknown: %+v", s)
	}
	record(t, db, hub, id, ViaInit)
	if s := read(); s.State != "recorded" || s.Note != "" || s.Upstream != nil {
		t.Fatalf("recorded: %+v", s)
	}
	recorded := recordedMain(t, db, id)
	commitIn(t, hub, "one")
	commitIn(t, hub, "two")
	if s := read(); s.State != "ahead" || s.Ahead != 2 || s.Note != "main is 2 commits ahead of the forge's record at "+strs.ShortSHA(recorded) {
		t.Fatalf("ahead: %+v", s)
	}
	for _, claim := range []string{"outside", "by "} {
		if strings.Contains(read().Note, claim) {
			t.Fatalf("the note attributes the move: %q", read().Note)
		}
	}
	rewriteMain(t, hub)
	if s := read(); s.State != "rewritten" || s.Note != "the recorded "+strs.ShortSHA(recorded)+" is no longer in main's history" {
		t.Fatalf("rewritten: %+v", s)
	}
	if s, err := ReadMainStanding(ctx, db, hub, id, "missing"); err != nil || s.State != "unreadable" || s.Note != "could not read main" {
		t.Fatalf("unreadable: %+v %v", s, err)
	}
}

// The last fetched upstream is compared with the record, both ways, and a
// missing tracking ref is unknown, never equal.
func TestMainStandingComparesTheLastFetchedUpstream(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, id, hub := mainProject(t)
	gittest.Run(t, hub, "remote", "add", "origin", t.TempDir())
	record(t, db, hub, id, ViaInit)
	upstream := func() *UpstreamMain {
		t.Helper()
		s, err := ReadMainStanding(ctx, db, hub, id, "main")
		if err != nil || s.Upstream == nil {
			t.Fatalf("upstream: %+v %v", s, err)
		}
		return s.Upstream
	}
	if u := upstream(); u.Known || u.Note != "last fetched upstream: unknown" {
		t.Fatalf("missing tracking ref: %+v", u)
	}
	base := Ref(ctx, hub, "refs/heads/main")
	gittest.Run(t, hub, "update-ref", "refs/remotes/origin/main", base)
	if u := upstream(); !u.Known || u.Ahead+u.Behind != 0 || u.Note != "" {
		t.Fatalf("equal: %+v", u)
	}
	gittest.RunAsTester(t, hub, "checkout", "-q", "-b", "upstream")
	pushed := commitIn(t, hub, "pushed straight to the upstream")
	gittest.Run(t, hub, "checkout", "-q", "main")
	gittest.Run(t, hub, "update-ref", "refs/remotes/origin/main", pushed)
	if u := upstream(); u.Ahead != 1 || u.Behind != 0 || u.Note != "last fetched upstream has 1 commit the forge's record does not" {
		t.Fatalf("upstream ahead: %+v", u)
	}
	commitIn(t, hub, "a forge landing")
	record(t, db, hub, id, ViaLanding)
	if u := upstream(); u.Ahead != 1 || u.Behind != 1 || u.Note != "last fetched upstream has 1 commit the forge's record does not and lacks 1 commit of the forge's record" {
		t.Fatalf("diverged: %+v", u)
	}
	gittest.Run(t, hub, "update-ref", "refs/remotes/origin/main", base)
	if u := upstream(); u.Ahead != 0 || u.Behind != 1 || u.Note != "last fetched upstream lacks 1 commit of the forge's record" {
		t.Fatalf("upstream behind: %+v", u)
	}
}

// commitOnHubMain moves the fixture hub's main forward, as a merge would.
func commitOnHubMain(t *testing.T, source Source, message string) string {
	t.Helper()
	return commitIn(t, source.Repository, message)
}

// A landing's settlement records the change's landed commit and main as read
// back. When recovery settles an older merge A after another door recorded a
// later main B, the change lands as A and main stays B.
func TestLandingRecordsMainAsReadBack(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	p, store := pendingMergeFixture(t)
	merged := commitOnHubMain(t, p.source, "the landing's merge")
	later := commitOnHubMain(t, p.source, "a later forge move")
	record(t, store.db, p.source.Repository, p.source.ProjectID, ViaGenesis)
	if err := p.settle(ctx, merged); err != nil {
		t.Fatal(err)
	}
	if sha := fixtureChangeSHA(t, store.db, p.work.(*fixtureClaim).changeID); sha != merged {
		t.Fatalf("landed as %q, want the merge %q", sha, merged)
	}
	if m, _, _ := ReadMain(ctx, store.db, p.source.ProjectID); m.SHA != later || m.Via != string(ViaGenesis) {
		t.Fatalf("recorded main %+v, want %s kept by genesis", m, later)
	}

	// With no record yet, the landing records main as read back, not its
	// own (older) merge result.
	q, other := pendingMergeFixture(t)
	result := commitOnHubMain(t, q.source, "merge")
	tip := commitOnHubMain(t, q.source, "a later move whose record failed")
	if err := q.settle(ctx, result); err != nil {
		t.Fatal(err)
	}
	if m, _, _ := ReadMain(ctx, other.db, q.source.ProjectID); m.SHA != tip || m.Via != string(ViaLanding) || m.RunID != q.source.RunID {
		t.Fatalf("recorded main %+v, want %s by landing run %d", m, tip, q.source.RunID)
	}
}

// A local merge writes its intent before Git runs: when settlement fails
// after main moved, startup inspection finds the merge and records the
// landing and main together.
func TestStartupInspectionRecordsMainForAFailedLocalSettlement(t *testing.T) {
	t.Parallel()
	service, claim := serviceFixture(t)
	db := service.records.db
	var merged string
	driver := fixtureDriver{
		prepare: func(context.Context, Source, string) (Prepared, error) {
			return Prepared{Kind: LocalMerge, Expected: Ref(context.Background(), claim.source.Repository, "refs/heads/main")}, nil
		},
		execute: func(_ context.Context, s Source, _ Prepared, _ string) (Execution, error) {
			merged = commitOnHubMain(t, s, "local merge")
			dbtest.FailNextStatement(t, db, "UPDATE run_merges SET state")
			return Execution{Result: merged}, nil
		},
		inspect: func(context.Context, Source, Record) (Inspection, error) {
			return Inspection{Kind: Found, Result: merged}, nil
		},
	}
	if _, err := service.Merge(authorized(NewEffect), claim.source.RunID, driver); err == nil {
		t.Fatal("a failed settlement returned success")
	}
	if got := recordedMain(t, db, claim.source.ProjectID); got != "" {
		t.Fatalf("a failed settlement recorded main %q", got)
	}
	summary, err := service.InspectPending(authorized(Observe), driver)
	if err != nil || summary.Recorded != 1 {
		t.Fatalf("inspection: %+v %v", summary, err)
	}
	if got := recordedMain(t, db, claim.source.ProjectID); got != merged {
		t.Fatalf("recorded main %q after inspection, want %q", got, merged)
	}
	if sha := fixtureChangeSHA(t, db, claim.changeID); sha != merged {
		t.Fatalf("landed as %q, want %q", sha, merged)
	}
}

// A merge request's or an already-merged branch's result is observed with no
// intent written first: startup inspection cannot see a failed observation,
// the record stays as it was, and retrying the landing settles it.
func TestAFailedObservationIsSettledByRetryingTheLanding(t *testing.T) {
	t.Parallel()
	service, claim := serviceFixture(t)
	db := service.records.db
	merged := commitOnHubMain(t, claim.source, "merged by the provider")
	driver := fixtureDriver{
		prepare: func(context.Context, Source, string) (Prepared, error) {
			return Prepared{Kind: Existing, Result: merged}, nil
		},
		inspect: func(context.Context, Source, Record) (Inspection, error) {
			t.Fatal("inspection found an observation it has no intent for")
			return Inspection{}, nil
		},
	}
	dbtest.FailNextStatement(t, db, "INSERT INTO run_merges")
	if _, err := service.Merge(authorized(NewEffect), claim.source.RunID, driver); err == nil {
		t.Fatal("a failed observation returned success")
	}
	summary, err := service.InspectPending(authorized(Observe), driver)
	if err != nil || summary.Inspected != 0 {
		t.Fatalf("startup inspection: %+v %v", summary, err)
	}
	if got := recordedMain(t, db, claim.source.ProjectID); got != "" {
		t.Fatalf("an unsettled observation recorded main %q", got)
	}
	if result, err := service.Merge(authorized(NewEffect), claim.source.RunID, driver); err != nil || !result.Merged {
		t.Fatalf("retry: %+v %v", result, err)
	}
	if got := recordedMain(t, db, claim.source.ProjectID); got != merged {
		t.Fatalf("recorded main %q after the retry, want %q", got, merged)
	}
}
