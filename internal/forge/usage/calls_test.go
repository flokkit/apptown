package usage

import (
	"context"
	"sync"
	"testing"
	"time"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
	"flokkit.io/forge/internal/poll"
)

// callWorld is project 7's forge model calls, opened and settled through
// the usage owner.
type callWorld struct {
	db       *database.DB
	requests *Requests
	ledger   *Ledger
	// project is the project the calls are made for; a test that has to
	// start over takes a fresh one, so an abandoned attempt counts nowhere.
	project ids.ID
}

func newCallWorld(t *testing.T) *callWorld {
	t.Helper()
	db := testDB(t)
	return &callWorld{db: db, requests: NewRequests(db), ledger: New(db), project: idstest.N(7)}
}

func (w *callWorld) open(t *testing.T, pending time.Duration) ids.ID {
	t.Helper()
	id, err := w.requests.OpenCall(context.Background(), Request{Kind: "forge", Actor: "apptown-system", ProjectID: w.project, Provider: "native", Model: "judge"},
		Entry{Kind: "narrative", ProjectID: w.project, Actor: "apptown-system", Provider: "native", Model: "judge"}, pending)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func (w *callWorld) settle(t *testing.T, id ids.ID, u Totals, incomplete string) bool {
	t.Helper()
	settled, err := w.requests.SettleCall(context.Background(), id, 200, "complete", "{}", u, incomplete)
	if err != nil {
		t.Fatal(err)
	}
	return settled
}

// spent is what admission reads (no locks): settled lines, and pending ones
// whose time has run out as unknown.
func (w *callWorld) spent(t *testing.T) Totals {
	t.Helper()
	got, err := w.ledger.ProjectsTotal(context.Background(), nil, w.project)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// expired waits until the forge's clock has passed the project's
// pending lines' time: admission then reads them as unknown spend.
func (w *callWorld) expired(t *testing.T, unknown int64) {
	t.Helper()
	if !poll.Until(context.Background(), 2*time.Minute, 10*time.Millisecond, func() bool { return w.spent(t).Incomplete >= unknown }) {
		t.Fatalf("the pending line never expired: %+v", w.spent(t))
	}
}

// heldBeforeExpiry opens a call and, in a transaction it returns open,
// does first whatever must happen before the call's time runs out. A
// loaded machine can be slower than any fixed margin, so an attempt that
// came too late is abandoned on a fresh project and tried again with a
// longer time; only the outcome of the ordering is asserted, never a
// duration.
func (w *callWorld) heldBeforeExpiry(t *testing.T, first func(*database.Tx, ids.ID) bool) (ids.ID, *database.Tx) {
	t.Helper()
	for pending := 300 * time.Millisecond; pending <= 5*time.Minute; pending *= 2 {
		id := w.open(t, pending)
		tx, err := w.db.Begin(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if first(tx, id) {
			t.Cleanup(func() { _ = tx.Rollback() })
			return id, tx
		}
		_ = tx.Rollback()
		w.project = idstest.N(999999)
	}
	t.Fatal("never in time, even with five minutes")
	return ids.Nil, nil
}

// lockIn is what a Continue acknowledges, in a transaction of its own.
func (w *callWorld) lockIn(t *testing.T) int64 {
	t.Helper()
	var n int64
	if err := w.db.InTx(context.Background(), func(tx *database.Tx) error {
		var err error
		n, err = w.ledger.LockInUnknownIn(context.Background(), tx, w.project)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

var paid = Totals{Input: 10, Output: 5, Total: 15, CostMicros: 2000}

// A forge call's spend line is written before it is sent and counts
// nothing while pending. It settles once: a replay changes nothing, and a
// second provider call is a line of its own. A line whose time has run out
// counts as unknown to admission at once, and a lock-in makes that
// permanent: a settlement after it is refused. At startup, every pending
// line is locked in, expired or not.
//
// Kills: a pending line read as unknown before its time (every call in
// flight would hold an eval); a replayed settlement counted twice; a late
// settlement turning locked-in unknown spend into known; a restart leaving
// cut-off calls pending.
func TestAForgeCallsSpendLineSettlesOnceOrStaysUnknown(t *testing.T) {
	t.Parallel()
	w := newCallWorld(t)
	a := w.open(t, time.Hour)
	if got := w.spent(t); got != (Totals{}) {
		t.Fatalf("a pending call counts nothing yet: %+v", got)
	}
	if !w.settle(t, a, paid, "") || w.settle(t, a, Totals{Input: 99, Total: 99}, "") {
		t.Fatal("a call settles once, and a replay is refused")
	}
	b := w.open(t, time.Hour)
	if !w.settle(t, b, paid, "") {
		t.Fatal("a second call settles on its own line")
	}
	if got := w.spent(t); got.Total != 30 || got.CostMicros != 4000 || got.Incomplete != 0 {
		t.Fatalf("two calls, each once: %+v", got)
	}

	// A time of zero has run out by the next read of the forge's clock.
	late := w.open(t, 0)
	if got := w.spent(t); got.Incomplete != 1 {
		t.Fatalf("a pending line past its time is unknown spend: %+v", got)
	}
	if n := w.lockIn(t); n != 1 {
		t.Fatalf("locked in: %d", n)
	}
	if w.settle(t, late, paid, "") {
		t.Fatal("a settlement after the lock-in was accepted")
	}
	if got := w.spent(t); got.Incomplete != 1 || got.Total != 30 {
		t.Fatalf("locked-in unknown spend stays unknown: %+v", got)
	}

	cut := w.open(t, time.Hour)
	if _, err := w.requests.MarkInterrupted(context.Background()); err != nil {
		t.Fatal(err)
	}
	if w.settle(t, cut, paid, "") || w.lockIn(t) != 2 {
		t.Fatal("a call cut off by a restart is locked in as unknown")
	}
}

// The row lock decides between a settlement and a lock-in, so a lock-in
// never acknowledges a line that then settles complete, and a line locked
// in can never settle. Two connections, a real Postgres:
//
//   - a: a complete settlement updates A's line before its time runs out,
//     and its transaction is held open past that time. Admission meanwhile
//     may read A as unknown; the lock-in waits on A's row and, once the
//     settlement commits, does not count it.
//   - b: the lock-in commits first; the late settlement changes nothing.
//   - c: a settlement transaction begun before the time ran out, whose
//     update comes after a lock-in, changes nothing.
//
// In each, after every transaction has ended, the locked-in count is never
// above the unknown lines that can no longer change, and it never falls.
//
// Kills: a lock-in that counts a pending row without taking its lock (a);
// a settlement that ignores a lock-in (b, c).
func TestSettlementAndLockInAreDecidedOnTheRow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	t.Run("a: settlement first, held past expiry", func(t *testing.T) {
		t.Parallel()
		w := newCallWorld(t)
		_, tx := w.heldBeforeExpiry(t, func(tx *database.Tx, a ids.ID) bool {
			settled, err := settleCallIn(ctx, tx, a, 200, "complete", "{}", paid, "", time.Now())
			if err != nil {
				t.Fatal(err)
			}
			return settled
		})
		// Admission reads the expired pending version as unknown.
		w.expired(t, 1)
		counted := make(chan int64)
		go func() {
			var n int64
			_ = w.db.InTx(ctx, func(tx *database.Tx) error {
				var err error
				n, err = w.ledger.LockInUnknownIn(ctx, tx, w.project)
				return err
			})
			counted <- n
		}()
		select {
		case n := <-counted:
			t.Fatalf("the lock-in did not wait for the row: counted %d", n)
		case <-time.After(200 * time.Millisecond):
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		if n := <-counted; n != 0 {
			t.Fatalf("the lock-in acknowledged a line that settled complete: %d", n)
		}
		if got := w.spent(t); got.Incomplete != 0 || got.Total != 15 {
			t.Fatalf("A settled complete: %+v", got)
		}
		b := w.open(t, time.Hour)
		w.settle(t, b, Totals{}, "the provider reported no usage")
		if n := w.lockIn(t); n != 1 {
			t.Fatalf("a later unknown charge exceeds the acknowledgement of 0: %d", n)
		}
	})
	t.Run("b: lock-in first", func(t *testing.T) {
		t.Parallel()
		w := newCallWorld(t)
		a := w.open(t, 0)
		if n := w.lockIn(t); n != 1 {
			t.Fatalf("locked in: %d", n)
		}
		if w.settle(t, a, paid, "") {
			t.Fatal("a late settlement changed a locked-in line")
		}
		if n := w.lockIn(t); n != 1 || w.spent(t).Incomplete != 1 {
			t.Fatalf("the count fell: %d %+v", n, w.spent(t))
		}
	})
	t.Run("c: settlement begun before expiry, updating after a lock-in", func(t *testing.T) {
		t.Parallel()
		w := newCallWorld(t)
		// The transaction is under way before A's time runs out: its own
		// read, at its start time, still sees A pending.
		a, tx := w.heldBeforeExpiry(t, func(tx *database.Tx, _ ids.ID) bool {
			got, err := w.ledger.ProjectsTotal(ctx, tx, w.project)
			if err != nil {
				t.Fatal(err)
			}
			return got.Incomplete == 0
		})
		w.expired(t, 1)
		if n := w.lockIn(t); n != 1 {
			t.Fatalf("locked in: %d", n)
		}
		if settled, err := settleCallIn(ctx, tx, a, 200, "complete", "{}", paid, "", time.Now()); err != nil || settled {
			t.Fatalf("a settlement after the lock-in: %v %v", settled, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		if n := w.lockIn(t); n != 1 {
			t.Fatalf("the count fell: %d", n)
		}
	})
}

// A spend line's pending time runs on the forge's clock: with that clock
// moved two minutes past a one-minute call, admission reads the line as
// unknown, the settlement is refused and the lock-in counts it, though no
// real time has passed.
//
// Kills: the pending time judged by the database's clock, at admission, at
// settlement or at the lock-in (none of them would see it run out).
func TestASpendLinesTimeRunsOutByTheForgesClock(t *testing.T) {
	t.Parallel()
	w := newCallWorld(t)
	var mu sync.Mutex
	at := time.Now().Add(time.Hour) // the forge's clock ahead of the database's
	now := func() time.Time { mu.Lock(); defer mu.Unlock(); return at }
	w.requests.Now, w.ledger.Now = now, now
	late := w.open(t, time.Minute)
	if got := w.spent(t); got.Incomplete != 0 {
		t.Fatalf("a line still in its time counts as unknown: %+v", got)
	}
	mu.Lock()
	at = at.Add(2 * time.Minute)
	mu.Unlock()
	if got := w.spent(t); got.Incomplete != 1 {
		t.Fatalf("a line past its time on AppTown's clock is not unknown: %+v", got)
	}
	if w.settle(t, late, paid, "") {
		t.Fatal("a settlement past the line's time was accepted")
	}
	if n := w.lockIn(t); n != 1 {
		t.Fatalf("locked in: %d", n)
	}
}
