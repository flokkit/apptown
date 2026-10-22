package deployment

import (
	"errors"
	"testing"
)

func TestReleaseSettlesOnceFromReleasing(t *testing.T) {
	t.Parallel()
	if got, err := Releasing.Settled(AttemptDone); err != nil || got != Released {
		t.Fatalf("done releases: %s %v", got, err)
	}
	for _, o := range []AttemptState{AttemptFailed, AttemptSuperseded} {
		if got, err := Releasing.Settled(o); err != nil || got != ReleaseFailed {
			t.Fatalf("%s fails the release: %s %v", o, got, err)
		}
	}
	if _, err := Releasing.Settled(AttemptRunning); !errors.Is(err, ErrReleaseTransition) {
		t.Fatalf("a running attempt settles nothing: %v", err)
	}
	for _, ended := range []ReleaseState{Released, ReleaseFailed} {
		if _, err := ended.Settled(AttemptDone); !errors.Is(err, ErrReleaseTransition) {
			t.Fatalf("%s never settles again: %v", ended, err)
		}
		if _, err := ended.Interrupted(); !errors.Is(err, ErrReleaseTransition) {
			t.Fatalf("%s cannot be interrupted: %v", ended, err)
		}
		if ended.Open() {
			t.Fatalf("%s is not open", ended)
		}
	}
	if got, err := Releasing.Interrupted(); err != nil || got != ReleaseFailed {
		t.Fatalf("an interrupted release failed: %s %v", got, err)
	}
}

func TestReleaseStateStorage(t *testing.T) {
	t.Parallel()
	var zero ReleaseState
	if _, err := zero.Value(); !errors.Is(err, ErrReleaseState) {
		t.Fatalf("the zero value cannot be stored: %v", err)
	}
	var s ReleaseState
	for _, word := range []string{"releasing", "released", "failed"} {
		if err := s.Scan(word); err != nil || s.String() != word {
			t.Fatalf("scan %q: %s %v", word, s, err)
		}
	}
	if err := s.Scan("superseded"); !errors.Is(err, ErrReleaseState) {
		t.Fatalf("an unknown stored word is refused: %v", err)
	}
}
