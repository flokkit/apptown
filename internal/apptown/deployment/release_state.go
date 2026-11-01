package deployment

import (
	"database/sql/driver"
	"errors"
	"fmt"

	"github.com/flokkit/apptown/internal/enum"
)

// ReleaseState is a release's lifecycle: releasing while its attempt runs,
// then released or failed. A release settles once; a repeated settlement
// with the same outcome is acknowledged by the caller, never rewritten.
type ReleaseState uint8

const (
	Releasing ReleaseState = iota + 1
	Released
	ReleaseFailed
)

// releaseStateCodec spells each ReleaseState in storage and JSON.
var releaseStateCodec = enum.Set[ReleaseState]{
	Names:     []string{"releasing", "released", "failed"},
	Invalid:   "invalid-release-state",
	Err:       errReleaseState,
	ScanLabel: "value",
}

var (
	errReleaseState      = errors.New("invalid release state")
	errReleaseTransition = errors.New("invalid release transition")
)

func (s ReleaseState) String() string { return releaseStateCodec.String(s) }

// open reports whether the release is still waiting on its attempt.
func (s ReleaseState) open() bool { return s == Releasing }

// settled is the rule when the release's attempt ends: done releases it;
// failed or superseded fails it. Only a releasing release settles.
func (s ReleaseState) settled(outcome AttemptState) (ReleaseState, error) {
	if !s.open() {
		return 0, fmt.Errorf("%w: release is already %s", errReleaseTransition, s)
	}
	switch outcome {
	case AttemptDone:
		return Released, nil
	case AttemptFailed, attemptSuperseded:
		return ReleaseFailed, nil
	default:
		return 0, fmt.Errorf("%w: an attempt that is %s settles nothing", errReleaseTransition, outcome)
	}
}

// ForOutcome names the settled state an ended attempt asks for, without
// checking the current state; SettledState checks it.
func releaseOutcome(outcome AttemptState) (ReleaseState, error) { return Releasing.settled(outcome) }

func parseReleaseState(text string) (ReleaseState, error) { return releaseStateCodec.Parse(text) }

func (s ReleaseState) Value() (driver.Value, error) { return releaseStateCodec.Value(s) }

func (s *ReleaseState) Scan(value any) error { return releaseStateCodec.Scan(s, value) }

func (s ReleaseState) MarshalJSON() ([]byte, error) { return releaseStateCodec.JSON(s) }

func (s *ReleaseState) UnmarshalJSON(data []byte) error {
	return releaseStateCodec.ReadJSON(s, data)
}
