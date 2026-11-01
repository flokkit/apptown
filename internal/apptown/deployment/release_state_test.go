package deployment

import (
	"errors"
	"testing"
)

func TestReleaseSettlesOnceFromReleasing(t *testing.T) {
	t.Parallel()
	if got, err := Releasing.settled(AttemptDone); err != nil || got != Released {
		t.Fatalf("done releases: %s %v", got, err)
	}
	for _, o := range []AttemptState{AttemptFailed, attemptSuperseded} {
		if got, err := Releasing.settled(o); err != nil || got != ReleaseFailed {
			t.Fatalf("%s fails the release: %s %v", o, got, err)
		}
	}
	if _, err := Releasing.settled(AttemptRunning); !errors.Is(err, errReleaseTransition) {
		t.Fatalf("a running attempt settles nothing: %v", err)
	}
	for _, ended := range []ReleaseState{Released, ReleaseFailed} {
		if _, err := ended.settled(AttemptDone); !errors.Is(err, errReleaseTransition) {
			t.Fatalf("%s never settles again: %v", ended, err)
		}

		if ended.open() {
			t.Fatalf("%s is not open", ended)
		}
	}

}

func TestReleaseStateStorage(t *testing.T) {
	t.Parallel()
	var zero ReleaseState
	if _, err := zero.Value(); !errors.Is(err, errReleaseState) {
		t.Fatalf("the zero value cannot be stored: %v", err)
	}
	var s ReleaseState
	for _, word := range []string{"releasing", "released", "failed"} {
		if err := s.Scan(word); err != nil || s.String() != word {
			t.Fatalf("scan %q: %s %v", word, s, err)
		}
	}
	if err := s.Scan("superseded"); !errors.Is(err, errReleaseState) {
		t.Fatalf("an unknown stored word is refused: %v", err)
	}
}
