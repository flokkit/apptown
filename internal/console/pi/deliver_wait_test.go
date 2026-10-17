package pi

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"flokkit.io/forge/internal/console"
)

// Delivering words presses Enter until the session's record shows them;
// the wait between presses ends with the request that asked.
func TestDeliveringWordsStopsWaitingWhenItsRequestEnds(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	r := New(Config{Run: func(_ context.Context, args ...string) ([]byte, error) {
		// The words are typed and Enter is pressed; the request then ends,
		// and the record (an empty session folder) never shows them.
		if slices.Contains(args, "Enter") {
			cancel()
		}
		return nil, nil
	}})
	defer r.Close()
	done := make(chan error, 1)
	go func() { done <- r.Deliver(ctx, console.Launch{ID: "c1", Dir: t.TempDir()}, "hello") }()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("a delivery whose request ended answered %v", err)
		}
	case <-time.After(10 * time.Second): // the presses take 17 s
		t.Fatal("the wait between presses outlived the request")
	}
}
