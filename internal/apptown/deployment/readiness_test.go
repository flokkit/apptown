package deployment

import (
	"errors"
	"testing"
)

func TestReadinessFollowsTheAttempt(t *testing.T) {
	t.Parallel()
	for _, from := range []Readiness{Reconciled, reconciling, ReadinessUncertain, ReadinessFailed} {
		if got, err := from.admitted(); err != nil || got != reconciling {
			t.Fatalf("admitting from %s: %s %v", from, got, err)
		}
	}
	for _, from := range []Readiness{reconciling, ReadinessUncertain} {
		if got, err := from.settled(AttemptDone); err != nil || got != Reconciled {
			t.Fatalf("%s settled done: %s %v", from, got, err)
		}
		if got, err := from.settled(AttemptFailed); err != nil || got != ReadinessFailed {
			t.Fatalf("%s settled failed: %s %v", from, got, err)
		}
		if got, err := from.lost(); err != nil || got != ReadinessUncertain {
			t.Fatalf("%s lost: %s %v", from, got, err)
		}
		if _, err := from.settled(attemptSuperseded); !errors.Is(err, errReadinessTransition) {
			t.Fatalf("a superseded attempt settles nothing: %v", err)
		}
	}
	for _, idle := range []Readiness{Reconciled, ReadinessFailed} {
		if _, err := idle.settled(AttemptDone); !errors.Is(err, errReadinessTransition) {
			t.Fatalf("%s has no attempt to settle: %v", idle, err)
		}
		if _, err := idle.lost(); !errors.Is(err, errReadinessTransition) {
			t.Fatalf("%s has no attempt to lose: %v", idle, err)
		}
	}
}

func TestReadinessStorage(t *testing.T) {
	t.Parallel()
	var zero Readiness
	if _, err := zero.Value(); !errors.Is(err, errReadiness) {
		t.Fatalf("the zero value cannot be stored: %v", err)
	}
	var s Readiness
	for _, word := range []string{"reconciled", "reconciling", "uncertain", "failed"} {
		if err := s.Scan(word); err != nil || s.String() != word {
			t.Fatalf("scan %q: %s %v", word, s, err)
		}
	}
	if err := s.Scan("ready"); !errors.Is(err, errReadiness) {
		t.Fatalf("an unknown stored word is refused: %v", err)
	}
}
