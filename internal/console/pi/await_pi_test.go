package pi

import (
	"context"
	"testing"
	"testing/synctest"
	"time"
)

// Waiting for pi ends with the request: a Docker question that never
// answers is cancelled when the page that asked goes away, not held until
// the runtime shuts down.
//
// Kills: the readiness question run under the runtime's own context.
func TestWaitingForPiEndsWithTheRequest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		entered := make(chan struct{})
		r := New(Config{Run: func(probeCtx context.Context, _ ...string) ([]byte, error) {
			close(entered)
			<-probeCtx.Done()
			return nil, probeCtx.Err()
		}})
		defer r.Close()
		done := make(chan error, 1)
		go func() { done <- r.awaitPi(ctx, "review") }()
		<-entered
		cancel()
		synctest.Wait()
		select {
		case <-done:
		default:
			t.Error("request cancellation left the readiness probe blocked until runtime shutdown")
		}
		r.Close()
		synctest.Wait()
	})
}

// Waiting for pi ends after ten minutes even if a Docker question never
// answers.
//
// Kills: the readiness question without the wait's own deadline.
func TestWaitingForPiEndsAfterTenMinutes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := New(Config{Run: func(probeCtx context.Context, _ ...string) ([]byte, error) {
			<-probeCtx.Done()
			return nil, probeCtx.Err()
		}})
		defer r.Close()
		done := make(chan error, 1)
		go func() { done <- r.awaitPi(t.Context(), "review") }()
		<-time.After(piStartWait + time.Second) // the bubble's time passes the bound
		synctest.Wait()
		select {
		case <-done:
		default:
			t.Error("the ten-minute bound cannot interrupt a readiness probe")
		}
		r.Close()
		synctest.Wait()
	})
}
