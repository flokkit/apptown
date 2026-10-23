package jobs

import (
	"context"
	"testing"
	"time"

	"github.com/riverqueue/river/rivertype"
)

// A job waits before its work while AppTown holds new work, and runs once
// it is released; a job whose context ends first does not run.
func TestAJobWaitsWhileNewWorkIsHeld(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	m := &jobHold{hold: func(ctx context.Context) error {
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	ran := make(chan struct{}, 2)
	inner := func(context.Context) error { ran <- struct{}{}; return nil }
	done := make(chan error, 1)
	go func() { done <- m.Work(context.Background(), &rivertype.JobRow{}, inner) }()
	select {
	case <-ran:
		t.Fatal("a job ran while new work was held")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	held := &jobHold{hold: func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }}
	if err := held.Work(ctx, &rivertype.JobRow{}, inner); err == nil {
		t.Fatal("a job whose context ended ran")
	}
	if len(ran) != 1 {
		t.Fatalf("the job ran %d times", len(ran)+1)
	}
	if len(middleware(Options{Hold: m.hold})) != 1 {
		t.Fatal("a queue given a hold does not wait on it")
	}
}
