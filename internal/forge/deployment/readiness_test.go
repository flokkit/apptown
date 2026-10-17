package deployment

import (
	"errors"
	"testing"
)

func TestReadinessFollowsTheAttempt(t *testing.T) {
	t.Parallel()
	for _, from := range []Readiness{Reconciled, Reconciling, ReadinessUncertain, ReadinessFailed, NeedsReconcile} {
		if got, err := from.Admitted(); err != nil || got != Reconciling {
			t.Fatalf("admitting from %s: %s %v", from, got, err)
		}
	}
	for _, from := range []Readiness{Reconciling, ReadinessUncertain} {
		if got, err := from.Settled(AttemptDone); err != nil || got != Reconciled {
			t.Fatalf("%s settled done: %s %v", from, got, err)
		}
		if got, err := from.Settled(AttemptFailed); err != nil || got != ReadinessFailed {
			t.Fatalf("%s settled failed: %s %v", from, got, err)
		}
		if got, err := from.Lost(); err != nil || got != ReadinessUncertain {
			t.Fatalf("%s lost: %s %v", from, got, err)
		}
		if _, err := from.Settled(AttemptSuperseded); !errors.Is(err, ErrReadinessTransition) {
			t.Fatalf("a superseded attempt settles nothing: %v", err)
		}
	}
	for _, idle := range []Readiness{Reconciled, ReadinessFailed, NeedsReconcile} {
		if _, err := idle.Settled(AttemptDone); !errors.Is(err, ErrReadinessTransition) {
			t.Fatalf("%s has no attempt to settle: %v", idle, err)
		}
		if _, err := idle.Lost(); !errors.Is(err, ErrReadinessTransition) {
			t.Fatalf("%s has no attempt to lose: %v", idle, err)
		}
	}
}

func TestReadinessStorage(t *testing.T) {
	t.Parallel()
	var zero Readiness
	if _, err := zero.Value(); !errors.Is(err, ErrReadiness) {
		t.Fatalf("the zero value cannot be stored: %v", err)
	}
	var s Readiness
	for _, word := range []string{"reconciled", "reconciling", "uncertain", "failed", "needs-reconcile"} {
		if err := s.Scan(word); err != nil || s.String() != word {
			t.Fatalf("scan %q: %s %v", word, s, err)
		}
	}
	if err := s.Scan("ready"); !errors.Is(err, ErrReadiness) {
		t.Fatalf("an unknown stored word is refused: %v", err)
	}
}
