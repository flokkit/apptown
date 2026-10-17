// Package clock is where an owner's real clock comes from. An owner reads
// the time only through a now func() time.Time it was given, so a test can
// set the time; Or gives it the real clock when it was given none. System
// is the one named exception. This is the one package the clock rule
// (.golangci.yml) lets name time.Now.
package clock

import "time"

// Or is now, or the real clock when now is nil.
func Or(now func() time.Time) func() time.Time {
	if now != nil {
		return now
	}
	return time.Now
}

// System is the operating system's clock, for comparing with a time the
// operating system stamped: a file's modification time. That is its only
// legitimate use. A time an owner writes or judges comes from the owner's
// clock (Or), which a test can move; a file's mtime cannot be moved with it.
func System() time.Time { return time.Now() }
