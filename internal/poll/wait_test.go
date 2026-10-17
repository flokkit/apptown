package poll

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A wait that runs its course answers nil.
func TestWaitAnswersNilWhenItsTimePasses(t *testing.T) {
	t.Parallel()
	if err := Wait(context.Background(), time.Millisecond); err != nil {
		t.Fatalf("a wait that ran its course answered %v", err)
	}
}

// A context that ends during the wait ends it at once, with the context's
// error; one already ended is not waited on at all.
func TestWaitEndsWithItsContext(t *testing.T) {
	t.Parallel()
	// The context ends while the wait is under way: its deadline is after
	// the wait began.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Wait(ctx, time.Hour) }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("a wait whose context ended answered %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the context ending did not end the wait")
	}
	if err := Wait(ctx, time.Hour); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a wait on an ended context answered %v", err)
	}
}
