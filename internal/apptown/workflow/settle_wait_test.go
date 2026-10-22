package workflow

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A settlement's tries stop when its context ends between two of them: the
// write's failure is returned, and the wait is not sat out.
func TestASettlementStopsTryingWhenItsContextEnds(t *testing.T) {
	t.Parallel()
	failing := errors.New("transient write failure")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	tries := 0
	done := make(chan error, 1)
	go func() {
		done <- Settlement{Tries: 3, Wait: time.Hour}.Retry(ctx, func() error { tries++; return failing })
	}()
	select {
	case err := <-done:
		if !errors.Is(err, failing) || !errors.Is(err, context.DeadlineExceeded) || tries != 1 {
			t.Fatalf("after %d tries: %v; want the write's failure, once, and the context's end", tries, err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the settlement's wait outlived its context")
	}
}
