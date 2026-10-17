// Package poll waits for a condition that nothing announces: a container
// turning healthy, a daemon starting to answer. It is the one loop for that,
// so every wait is bounded, checks the condition once more at its deadline,
// and stops early when its context ends. Wait is the pause between two
// attempts of a loop that is not a condition — a retry's backoff, a follow
// loop's interval — and ends early with its context too. Neither reads a
// clock: each is timed by timers, so a test's synctest bubble times it too.
// A test's check that something does not happen is polltest.Holds.
package poll

import (
	"context"
	"time"
)

// Until checks cond at once and then every interval until it holds, the
// timeout passes or ctx ends. It reports whether cond held. The last check
// is made at the deadline, so a condition that comes true during the final
// interval is still seen.
func Until(ctx context.Context, timeout, interval time.Duration, cond func() bool) bool {
	return watch(ctx, timeout, interval, cond)
}

// watch checks cond at once and then, after each check, once interval has
// passed, until it holds, d passes (the last check is made then) or ctx
// ends. It reports whether cond held.
func watch(ctx context.Context, d, interval time.Duration, cond func() bool) bool {
	end := time.NewTimer(d)
	defer end.Stop()
	for {
		if cond() {
			return true
		}
		if ctx.Err() != nil {
			return false
		}
		next := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			next.Stop()
			return false
		case <-end.C:
			next.Stop()
			return cond()
		case <-next.C:
		}
	}
}

// Wait waits for d, or until ctx ends, and returns ctx's error when it ended
// first (or had already ended); nil when d passed.
func Wait(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
