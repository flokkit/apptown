package poll

import (
	"context"
	"testing"
	"testing/synctest"
	"time"
)

func TestUntilReturnsAsSoonAsTheConditionHolds(t *testing.T) {
	calls := 0
	if !Until(context.Background(), time.Second, time.Millisecond, func() bool { calls++; return calls == 3 }) {
		t.Fatal("the condition held on the third check, but Until reported false")
	}
	if calls != 3 {
		t.Fatalf("checked %d times, want 3", calls)
	}
}

func TestUntilChecksOnceMoreAtTheDeadline(t *testing.T) {
	start := time.Now()
	calls := 0
	held := Until(context.Background(), 30*time.Millisecond, time.Hour, func() bool {
		calls++
		return time.Since(start) >= 30*time.Millisecond
	})
	if !held || calls != 2 {
		t.Fatalf("held=%v after %d checks; want the deadline check to see the condition", held, calls)
	}
}

func TestUntilGivesUpAtTheTimeout(t *testing.T) {
	start := time.Now()
	if Until(context.Background(), 20*time.Millisecond, 5*time.Millisecond, func() bool { return false }) {
		t.Fatal("a condition that never holds was reported as held")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("waited %s past a 20ms timeout", elapsed)
	}
}

func TestUntilStopsWhenTheContextEnds(t *testing.T) {
	// In a bubble the hour is the bubble's: a wait that did not end with its
	// context would show as an hour passing, at no cost.
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		calls := 0
		start := time.Now()
		held := Until(ctx, time.Hour, time.Hour, func() bool {
			calls++
			if calls == 1 {
				time.AfterFunc(10*time.Millisecond, cancel)
			}
			return false
		})
		if held || time.Since(start) > 5*time.Second {
			t.Fatalf("held=%v after %s; a cancelled wait must end at once and report false", held, time.Since(start))
		}
	})
}
