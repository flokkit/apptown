package tasks

import (
	"context"
	"testing"
	"time"
)

// The production caller passes time.Now().Add(timeout), without rounding it.
// Postgres represents a timestamptz at microsecond precision.
func TestAReservationIsComparedAtTheStoredPrecision(t *testing.T) {
	r, in := attemptFixture(t)
	in.Deadline = in.Deadline.Truncate(time.Microsecond).Add(123 * time.Nanosecond)
	got, err := r.Reserve(context.Background(), in)
	if err != nil {
		t.Fatalf("first reservation was rejected after its own insert: %v; input=%s stored=%s", err, in.Deadline.Format(time.RFC3339Nano), got.Deadline.Format(time.RFC3339Nano))
	}
	if _, err := r.Reserve(context.Background(), in); err != nil {
		t.Fatalf("identical reservation retry: %v", err)
	}
}
