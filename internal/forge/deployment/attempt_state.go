package deployment

import (
	"database/sql/driver"
	"errors"
	"fmt"

	"flokkit.io/forge/internal/enum"
)

// AttemptState is one deployment attempt's lifecycle. An attempt runs, may
// become uncertain when the process that ran it died before an outcome was
// recorded, and ends done, failed or superseded. Terminal states never
// change again; an outcome reported twice is acknowledged, never rewritten.
type AttemptState uint8

const (
	AttemptRunning AttemptState = iota + 1
	AttemptUncertain
	AttemptDone
	AttemptFailed
	AttemptSuperseded
)

// attemptStateCodec spells each AttemptState in storage and JSON.
var attemptStateCodec = enum.Set[AttemptState]{
	Names:     []string{"running", "uncertain", "done", "failed", "superseded"},
	Invalid:   "invalid-attempt-state",
	Err:       ErrAttemptState,
	ScanLabel: "value",
}

var (
	ErrAttemptState      = errors.New("invalid attempt state")
	ErrAttemptTransition = errors.New("invalid attempt transition")
)

func (s AttemptState) String() string { return attemptStateCodec.String(s) }

// Open reports whether the attempt may still receive an outcome.
func (s AttemptState) Open() bool { return s == AttemptRunning || s == AttemptUncertain }

// Terminal is the stored flag's meaning: no further outcome can be recorded.
func (s AttemptState) Terminal() bool {
	return s == AttemptDone || s == AttemptFailed || s == AttemptSuperseded
}

// Transition is the one rule for changing a stored attempt state. Running may
// become uncertain (the runner was lost) or end; uncertain may only end; an
// ended attempt stays ended.
func (s AttemptState) Transition(next AttemptState) (AttemptState, error) {
	if !s.Open() {
		return 0, fmt.Errorf("%w: attempt is already %s", ErrAttemptTransition, s)
	}
	switch next {
	case AttemptDone, AttemptFailed, AttemptSuperseded:
		return next, nil
	case AttemptUncertain:
		if s == AttemptRunning {
			return next, nil
		}
	}
	return 0, fmt.Errorf("%w: %s to %s", ErrAttemptTransition, s, next)
}

// Outcome names the ended state a completion or failure asks for.
func OutcomeState(failed bool) AttemptState {
	if failed {
		return AttemptFailed
	}
	return AttemptDone
}

func (s AttemptState) Value() (driver.Value, error) { return attemptStateCodec.Value(s) }

func (s *AttemptState) Scan(value any) error { return attemptStateCodec.Scan(s, value) }

func (s AttemptState) MarshalJSON() ([]byte, error) { return attemptStateCodec.JSON(s) }

func (s *AttemptState) UnmarshalJSON(data []byte) error {
	return attemptStateCodec.ReadJSON(s, data)
}
