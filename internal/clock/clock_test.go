package clock

import (
	"testing"
	"time"
)

// An owner given a clock keeps it; one given none reads the real time.
func TestOrKeepsAGivenClockAndOtherwiseReadsTheRealOne(t *testing.T) {
	t.Parallel()
	fixed := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	if got := Or(func() time.Time { return fixed })(); !got.Equal(fixed) {
		t.Fatalf("a given clock was replaced: %v", got)
	}
	before := time.Now()
	if got := Or(nil)(); got.Before(before) || got.Sub(before) > time.Minute {
		t.Fatalf("no clock did not read the real time: %v (before %v)", got, before)
	}
}
