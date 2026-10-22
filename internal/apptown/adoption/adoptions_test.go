package adoption

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestAdvanceRecordsRevisionsWithReceiptsAndSettlesDrift(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	ads := newAdoptions(db)
	pid := adoptionTestProject(t, db, "ad", "Ad")
	if err := db.InTx(context.Background(), func(tx *database.Tx) error {
		return InitializeIn(t.Context(), tx, pid, 0, "", "moved outside the door")
	}); err != nil {
		t.Fatal(err)
	}
	resolve := func(blocks ...ReceiptBlock) func(*database.Tx, *RecordedRevision, time.Time) (Receipt, error) {
		return func(_ *database.Tx, _ *RecordedRevision, _ time.Time) (Receipt, error) {
			r := EmptyReceipt()
			r.Guidance = append(r.Guidance, blocks...)
			return r, nil
		}
	}
	first, err := ads.Advance(t.Context(), pid, AdoptionEdit, "first", "alice", false, resolve(ReceiptBlock{Name: "lang/go", BlockID: idstest.N(1), Version: 1}))
	if err != nil || first.Revision != 1 || first.Kind != AdoptionEdit {
		t.Fatalf("first: %+v %v", first, err)
	}
	if cur := currentOf(t, db, pid); cur.Revision != 1 || cur.Drift != "" {
		t.Fatalf("first revision did not settle the aggregate: %+v", cur)
	}
	if n := receiptsWhere(t, db, pid, "adoption.recorded", func(e audit.Entry) bool { return e.Subject == "r1" && e.Actor == "alice" }); n != 1 {
		t.Fatalf("first revision receipts: %d", n)
	}

	// Unchanged: no new revision, the drift note still settles.
	if err := db.InTx(context.Background(), func(tx *database.Tx) error {
		return audit.At(time.Now()).RecordIn(context.Background(), tx, "", ids.Nil, "noise", "x", struct{}{})
	}); err != nil {
		t.Fatal(err)
	}
	if err := New(db, Dependencies{}).NoteDrift(t.Context(), pid, "drifted again"); err != nil {
		t.Fatal(err)
	}
	same, err := ads.Advance(t.Context(), pid, AdoptionEdit, "nothing new", "alice", false, resolve(ReceiptBlock{Name: "lang/go", BlockID: idstest.N(1), Version: 1}))
	if err != nil || same.ID != first.ID {
		t.Fatalf("unchanged: %+v %v", same, err)
	}
	if revs := revisionsOf(t, db, pid); len(revs) != 1 {
		t.Fatalf("unchanged advance recorded: %+v", revs)
	}
	if cur := currentOf(t, db, pid); cur.Drift != "" {
		t.Fatalf("drift note not settled: %+v", cur)
	}

	// A receipt that cannot be written rolls the revision back with it.
	dbtest.FailNextStatement(t, db, "INSERT INTO audit_log")
	if _, err := ads.Advance(t.Context(), pid, AdoptionEdit, "second", "alice", false, resolve(ReceiptBlock{Name: "lang/go", BlockID: idstest.N(1), Version: 2})); err == nil {
		t.Fatal("a revision without its receipt was reported as recorded")
	}
	if revs := revisionsOf(t, db, pid); len(revs) != 1 {
		t.Fatalf("revision without receipt recorded: %+v", revs)
	}
	if cur := currentOf(t, db, pid); cur.Revision != 1 {
		t.Fatalf("revision without receipt moved the aggregate: %+v", cur)
	}

	history, _ := ads.History(t.Context(), pid, 10)
	if len(history) != 1 || len(history[0].Receipt.Guidance) != 0 {
		t.Fatalf("history is light: %+v", history)
	}
	if _, err := ads.Current(t.Context(), idstest.N(999999)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("absence is sql.ErrNoRows: %v", err)
	}
}

// reviewRequests is the review requester as this owner sees it: it records
// what each request carried and whether Current held inside the recording
// transaction, and can refuse.
type reviewRequests struct {
	asked   []Recorded
	current []bool
	refuse  error
}

func (r *reviewRequests) RequestIn(ctx context.Context, _ *database.Tx, rec Recorded) error {
	ok, err := rec.Current(ctx)
	if err != nil {
		return err
	}
	r.asked, r.current = append(r.asked, rec), append(r.current, ok)
	return r.refuse
}

// A recorded change of a project's way of working requests that project's
// review in the same transaction, carrying the revision it recorded, which
// is current there. A reviewed no-op records nothing and requests nothing;
// creation (RecordIn) requests nothing; a refused request rolls the
// revision back with everything it wrote.
func TestARecordedRevisionRequestsItsProjectsReview(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	requests := &reviewRequests{}
	ads := newAdoptions(db)
	ads.reviews = requests
	pid := adoptionTestProject(t, db, "ad", "Ad")
	created := EmptyReceipt()
	if err := db.InTx(context.Background(), func(tx *database.Tx) error {
		return RecordIn(t.Context(), tx, pid, 1, AdoptionCreate, "project created", created, "alice", time.Now())
	}); err != nil {
		t.Fatal(err)
	}
	if len(requests.asked) != 0 {
		t.Fatalf("creation requested a review: %+v", requests.asked)
	}
	resolve := func(version int) func(*database.Tx, *RecordedRevision, time.Time) (Receipt, error) {
		return func(_ *database.Tx, _ *RecordedRevision, _ time.Time) (Receipt, error) {
			r := EmptyReceipt()
			r.Guidance = append(r.Guidance, ReceiptBlock{Name: "lang/go", BlockID: idstest.N(1), Version: version})
			return r, nil
		}
	}
	if _, err := ads.Advance(t.Context(), pid, AdoptionEdit, "adopt go", "alice", false, resolve(1)); err != nil {
		t.Fatal(err)
	}
	if len(requests.asked) != 1 || requests.asked[0].ProjectID() != pid || requests.asked[0].Revision() != 2 || !requests.current[0] {
		t.Fatalf("the change's request: %+v current %v", requests.asked, requests.current)
	}
	if _, err := ads.Advance(t.Context(), pid, AdoptionEdit, "nothing new", "alice", false, resolve(1)); err != nil || len(requests.asked) != 1 {
		t.Fatalf("a reviewed no-op requested a review: %v %+v", err, requests.asked)
	}
	requests.refuse = errors.New("the queue is unavailable")
	if _, err := ads.Advance(t.Context(), pid, AdoptionEdit, "go v2", "alice", false, resolve(2)); err == nil {
		t.Fatal("a revision whose review could not be requested was recorded")
	}
	if revs := revisionsOf(t, db, pid); len(revs) != 2 {
		t.Fatalf("the refused revision was kept: %+v", revs)
	}
	if cur := currentOf(t, db, pid); cur.Revision != 2 {
		t.Fatalf("the refused revision moved the aggregate: %+v", cur)
	}
	if n := receiptsWhere(t, db, pid, "adoption.recorded", func(e audit.Entry) bool { return e.Subject == "r3" }); n != 0 {
		t.Fatalf("the refused revision's receipt was kept: %d", n)
	}
	if current, err := (Recorded{}).Current(t.Context()); err != nil || current {
		t.Fatalf("a Recorded this owner did not build is current: %v %v", current, err)
	}
}
