// Package polltest is poll's converse for tests: the check that something
// does not happen for a while, and the bound for a wait whose length is not
// what a test checks. It lives apart from poll because nothing the
// apptown runs asks it (the reachability gate holds poll to what apptownd
// reaches).
package polltest

import (
	"context"
	"time"

	"github.com/flokkit/apptown/internal/poll"
)

// Holds checks cond at once and then every so often for d, and reports
// whether it stayed false throughout: false as soon as it turns true, and
// false when ctx ends before d has passed, since then the observation was
// not made. It is the check that something does not happen: a process that
// stays up, a file nobody writes. The last check is made at the end of d.
func Holds(ctx context.Context, d time.Duration, cond func() bool) bool {
	return !poll.Until(ctx, d, interval(d), cond) && ctx.Err() == nil
}

// interval is how often Holds looks: often enough that a short hold is
// looked at many times, never more than every millisecond or less than
// every 50 ms.
func interval(d time.Duration) time.Duration {
	return max(min(d/20, 50*time.Millisecond), time.Millisecond)
}

// HangGuard bounds a wait whose length is not what the test checks: the
// test binary's remaining time, less a tenth to report the failure (ten
// minutes when the binary has no deadline). A test's guarantee is the
// event it waits for; a few seconds of wall clock is not a guarantee, and on
// a loaded machine an ordinary Docker or Git call (or the first start of a
// freshly written executable, which macOS scans) can take longer.
func HangGuard(t interface{ Deadline() (time.Time, bool) }) time.Duration {
	deadline, ok := t.Deadline()
	if !ok {
		return 10 * time.Minute
	}
	return time.Until(deadline) * 9 / 10
}
