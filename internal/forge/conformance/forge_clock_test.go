package conformance

import (
	"context"
	"testing"
	"time"

	"flokkit.io/forge/internal/clock/clocktest"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/wow"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/stamp"
)

// An exception is stamped and expires by the forge's clock, not the wall
// clock or the database's: on a clock ninety days behind, an exception
// that ends in an hour is active and stamped with that clock's time, and
// it lapses when that clock passes its end. A review is stamped by it too.
func TestTheForgesClockStampsAndExpiresExceptions(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	id := createTestProject(t, db, "clocked", "Clocked", "main")
	at := time.Now().Add(-90 * 24 * time.Hour).UTC().Truncate(time.Second)
	clock := at
	s := New(db, nil, &testSource{head: "abc"}, func() time.Time { return clock })
	s.authority = testAuthority{}
	saved, err := s.CreateDisposition(caller("ops"), ExceptionRequest{ProjectID: id, Identifier: "CVE-1", Kind: AcceptedRisk, Justification: "until the fix ships", ExpiresAt: stamp.Of(at.Add(time.Hour))})
	if err != nil {
		t.Fatal(err)
	}
	if !stamp.Parse(saved.CreatedAt).Time.Equal(at) {
		t.Fatalf("an exception stamped %s, not %s", saved.CreatedAt, at)
	}
	active, err := s.Dispositions(caller("ops"), id, true)
	if err != nil || len(active) != 1 {
		t.Fatalf("an exception within its hour by the forge's clock: %+v %v", active, err)
	}
	review, err := s.BeginReview(caller("ops"), ReviewRequest{Projects: []ids.ID{id}})
	if err != nil {
		t.Fatal(err)
	}
	if !stamp.Parse(review.review.StartedAt).Time.Equal(at) {
		t.Fatalf("a review started %s, not %s", review.review.StartedAt, at)
	}
	clock = at.Add(2 * time.Hour)
	if active, err := s.Dispositions(caller("ops"), id, true); err != nil || len(active) != 0 {
		t.Fatalf("an exception past its end by the forge's clock: %+v %v", active, err)
	}
}

// An exception's creation and a verdict's confirmation take one reading of
// the forge's clock for the record and its receipt. On a clock that moves
// on a second at every reading, a receipt stamped by a second reading
// would say a later second than the record.
func TestAConformanceReceiptCarriesItsRecordsTime(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	id := createTestProject(t, db, "stepping", "Stepping", "main")
	block := stockCheckFixture(t, db, wow.StockBlock{Name: "prose", ChecksJSON: `[{"name":"readme-plain","evaluator":"llm","scope":"tree","rubric":"README is plain","files":["README.md"]}]`})
	pinCheckFixture(t, db, id, wow.ResolvedPin{BlockID: block, Name: "prose", Version: 1})
	clk := clocktest.NewStepping(time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC), time.Second)
	s := New(db, nil, &confirmationSource{head: "first-head"}, clk.Now)
	s.authority = testAuthority{}
	receiptAt := func(kind string) time.Time {
		t.Helper()
		got, err := audit.Recent(context.Background(), db, audit.Filter{KindPrefix: kind})
		if err != nil || len(got) == 0 {
			t.Fatalf("%s receipts: %+v %v", kind, got, err)
		}
		at, err := time.Parse(time.RFC3339Nano, got[0].At)
		if err != nil {
			t.Fatal(err)
		}
		return at
	}

	saved, err := s.CreateDisposition(caller("ops"), ExceptionRequest{ProjectID: id, Identifier: "CVE-1", Kind: AcceptedRisk, Justification: "until the fix ships"})
	if err != nil {
		t.Fatal(err)
	}
	if record, receipt := stamp.Parse(saved.CreatedAt).Time, receiptAt("disposition.created"); !record.Equal(receipt) {
		t.Errorf("an exception recorded at %v, its receipt at %v", record, receipt)
	}

	flag := confirmationVerdict(t, s, id, DoesNotConform)
	confirmed, err := s.Confirm(caller("bls"), Confirmation{VerdictID: flag.ID, Note: "yes"})
	if err != nil {
		t.Fatal(err)
	}
	if record, receipt := stamp.Parse(confirmed.CreatedAt).Time, receiptAt("conformance.confirmed"); !record.Equal(receipt) {
		t.Errorf("a confirmation recorded at %v, its receipt at %v", record, receipt)
	}
}
