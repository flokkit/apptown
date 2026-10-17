// Package polltest is poll's converse for tests: the check that something
// does not happen for a while. It lives apart from poll because nothing the
// forge runs asks it (the reachability gate holds poll to what forged
// reaches).
package polltest

import (
	"context"
	"time"

	"flokkit.io/forge/internal/poll"
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
