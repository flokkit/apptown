package workflow

import (
	"context"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/clock/clocktest"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/stamp"
)

// receiptTime is the newest receipt of kind, at the ledger's precision.
func receiptTime(t *testing.T, db *database.DB, kind string) time.Time {
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

func sameSecond(t *testing.T, what string, record, receipt time.Time) {
	t.Helper()
	if !record.Truncate(time.Second).Equal(receipt.Truncate(time.Second)) {
		t.Errorf("%s recorded at %v, its receipt at %v", what, record, receipt)
	}
}

// A Change operation takes one reading of AppTown's clock for what it
// stores and for its receipt. On a clock that moves on a second at every
// reading, a receipt stamped by a separate reading says a later second
// than the record it describes: an uplift's opening, a
// send-back's continued run and a Change's abandonment.
func TestAChangesReceiptCarriesItsRecordsTime(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	start := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)

	c, in := upliftFixture(t)
	c.clocked = clocked{clocktest.NewStepping(start, time.Second).Now}
	ch, err := c.openUplift(ctx, in, "alice")
	if err != nil {
		t.Fatal(err)
	}
	sameSecond(t, "an uplift", stamp.Parse(ch.CreatedAt).Time, receiptTime(t, c.db, "wow.uplift.opened"))

	s, source, _ := completedSendBackFixture(t)
	s.clocked = clocked{clocktest.NewStepping(start, time.Second).Now}
	result, err := s.sendBack(ctx, source.ChangeID, "again", "alice", "", source.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	continued, err := readRun(ctx, s.db, result.RunID)
	if err != nil {
		t.Fatal(err)
	}
	sameSecond(t, "a send-back", stamp.Parse(continued.CreatedAt).Time, receiptTime(t, s.db, "change.sendback"))

	owner, root := abandonFixture(t)
	owner.clocked = clocked{clocktest.NewStepping(start, time.Second).Now}
	if _, err := owner.Abandon(ctx, Abandonment{ChangeID: root.ChangeID, Who: "operator", Reason: "stop"}); err != nil {
		t.Fatal(err)
	}
	abandoned, err := readChange(ctx, owner.db, root.ChangeID)
	if err != nil {
		t.Fatal(err)
	}
	sameSecond(t, "an abandonment", abandoned.ClosedAt.Time, receiptTime(t, owner.db, "change.abandoned"))
}

// A send-back to a run still in its agent's stage retries the agent in
// place, inside the send-back: the run's move, the retry's events and the
// Change's receipt carry the send-back's one reading of AppTown's clock.
// On a clock that moves on a second at every reading, any separate reading
// (or the machine's clock) would say another second.
func TestAnInPlaceSendBackCarriesItsReading(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	retries, run, _, _ := retryFixture(t)
	clk := clocktest.NewStepping(time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC), time.Second)
	s := sendBackStore{db: retries.db, automatic: retries.automatic, abandonments: NewAbandonments(retries.db, abandonRecorderFunc(noAbandonEffects), &scm.RepositoryLocks{}), clocked: clocked{clk.Now}}
	reading := clk.Next()
	result, err := s.sendBack(ctx, run.ChangeID, "try again", "alice", "", run.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if result.RunID != run.ID {
		t.Fatalf("the send-back made run %d, not a retry in run %d", result.RunID, run.ID)
	}
	sameSecond(t, "the Change's receipt", reading, receiptTime(t, s.db, "change.sendback"))
	moved, err := readRun(ctx, s.db, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	sameSecond(t, "the run's move", reading, stamp.Parse(moved.UpdatedAt).Time)
	seen := map[string]bool{}
	for _, e := range storedRunEvents(t, s.db, run.ID) {
		if e.Kind == "step.retried" || e.Kind == "step.sendback" || e.Kind == "step.invalidated" {
			seen[e.Kind] = true
			sameSecond(t, e.Kind, reading, stamp.Parse(e.At).Time)
		}
	}
	if !seen["step.retried"] || !seen["step.sendback"] {
		t.Fatalf("the retry's events: %v", seen)
	}
}
