package polltest

import (
	"context"
	"testing"
	"testing/synctest"
	"time"
)

// In a bubble time is the bubble's: each hold below is observed for its
// whole length at no cost, and how long it took is exact.

// A condition that never turns true holds for the whole duration, and the
// hold takes that long.
func TestHoldsWhenNothingHappensForTheWholeDuration(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		start, checks := time.Now(), 0
		if !Holds(context.Background(), time.Hour, func() bool { checks++; return false }) {
			t.Fatal("a condition that never turned true did not hold")
		}
		if took := time.Since(start); took != time.Hour {
			t.Fatalf("an hour's hold took %s", took)
		}
		if checks < 20 {
			t.Fatalf("an hour's hold looked only %d times", checks)
		}
	})
}

// A hold fails as soon as the condition turns true, not at the end.
func TestHoldsFailsAsSoonAsSomethingHappens(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		if Holds(context.Background(), time.Hour, func() bool { return time.Since(start) >= time.Second }) {
			t.Fatal("a condition that turned true held")
		}
		if took := time.Since(start); took > time.Second+50*time.Millisecond {
			t.Fatalf("a hold broken after a second ended after %s", took)
		}
	})
}

// What happens in the last interval is still seen: the last check is at
// the end of the duration.
func TestHoldsLooksOnceMoreAtTheEnd(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		// It turns true after the last interval's look (every 50 ms) and
		// before the end, 1010 ms: only the look at the end sees it.
		start := time.Now()
		if Holds(context.Background(), 1010*time.Millisecond, func() bool { return time.Since(start) >= 1005*time.Millisecond }) {
			t.Fatal("a condition true at the end of the hold held")
		}
	})
}

// A hold whose context ends before its duration has passed was not
// observed, so it does not hold.
func TestHoldsIsNotClaimedWhenItsContextEnds(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		start := time.Now()
		if Holds(ctx, time.Hour, func() bool { return false }) {
			t.Fatal("a hold cut short by its context was claimed")
		}
		if took := time.Since(start); took != time.Second {
			t.Fatalf("a hold whose context ended after a second took %s", took)
		}
	})
}
