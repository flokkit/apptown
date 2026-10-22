package deployment

import (
	"errors"
	"testing"
)

func TestAttemptStateEndsOnceAndOnlyFromAnOpenState(t *testing.T) {
	t.Parallel()
	for _, end := range []AttemptState{AttemptDone, AttemptFailed, AttemptSuperseded} {
		for _, from := range []AttemptState{AttemptRunning, AttemptUncertain} {
			if got, err := from.Transition(end); err != nil || got != end {
				t.Fatalf("%s to %s: %s %v", from, end, got, err)
			}
		}
		for _, again := range []AttemptState{AttemptDone, AttemptFailed, AttemptSuperseded, AttemptUncertain, AttemptRunning} {
			if _, err := end.Transition(again); !errors.Is(err, ErrAttemptTransition) {
				t.Fatalf("an ended attempt stays %s; %s was allowed: %v", end, again, err)
			}
		}
		if !end.Terminal() || end.Open() {
			t.Fatalf("%s is terminal and not open", end)
		}
	}
}

func TestOnlyARunningAttemptBecomesUncertain(t *testing.T) {
	t.Parallel()
	if got, err := AttemptRunning.Transition(AttemptUncertain); err != nil || got != AttemptUncertain {
		t.Fatalf("running to uncertain: %s %v", got, err)
	}
	if _, err := AttemptUncertain.Transition(AttemptUncertain); !errors.Is(err, ErrAttemptTransition) {
		t.Fatalf("uncertain may only end: %v", err)
	}
	if _, err := AttemptUncertain.Transition(AttemptRunning); !errors.Is(err, ErrAttemptTransition) {
		t.Fatalf("an attempt never runs again: %v", err)
	}
	if OutcomeState(true) != AttemptFailed || OutcomeState(false) != AttemptDone {
		t.Fatal("an outcome is done or failed")
	}
}

func TestAttemptStateStorageAndZeroValue(t *testing.T) {
	t.Parallel()
	var zero AttemptState
	if _, err := zero.Value(); !errors.Is(err, ErrAttemptState) {
		t.Fatalf("the zero value cannot be stored: %v", err)
	}
	if _, err := zero.Transition(AttemptDone); !errors.Is(err, ErrAttemptTransition) {
		t.Fatalf("the zero value is not open: %v", err)
	}
	var s AttemptState
	for _, word := range []string{"running", "uncertain", "done", "failed", "superseded"} {
		if err := s.Scan([]byte(word)); err != nil || s.String() != word {
			t.Fatalf("scan %q: %s %v", word, s, err)
		}
	}
	if err := s.Scan("new"); !errors.Is(err, ErrAttemptState) {
		t.Fatalf("an unknown stored word is refused: %v", err)
	}
}
