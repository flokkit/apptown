package docker

import (
	"slices"
	"testing"
	"time"
)

// The reaper judges a spawned container's age by its AppTown.created label,
// so the label is stamped from the scope's clock, not the machine's.
func TestAContainersCreatedLabelIsTheScopesClock(t *testing.T) {
	t.Parallel()
	at := time.Date(2031, 2, 3, 4, 5, 6, 0, time.FixedZone("x", 3600))
	got := Scope{Now: func() time.Time { return at }}.Labels("service", "pv7")
	if !slices.Contains(got, "forge.created=2031-02-03T03:05:06Z") || !slices.Contains(got, "forge.class=service") || !slices.Contains(got, "forge.owner=pv7") {
		t.Fatalf("labels %q", got)
	}
	// An attached host's scope keeps the clock.
	if got := (Scope{Now: func() time.Time { return at }}).WithHost("ssh://h", "h", "").Labels("env", "x"); !slices.Contains(got, "forge.created=2031-02-03T03:05:06Z") {
		t.Fatalf("an attached host's labels %q", got)
	}
}
