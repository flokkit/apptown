package workflow

import (
	"database/sql/driver"
	"errors"
	"fmt"

	"github.com/flokkit/apptown/internal/enum"
)

// RunState is the lifecycle of one pinned workflow execution. Its zero value
// is invalid. Completion and abandonment are terminal; continuation creates a
// new identity. The workflow package owns the lifecycle rule.
type RunState uint8

const (
	RunRunning RunState = iota + 1
	RunDone
	RunAbandoned
)

// runStateCodec spells each RunState in storage and JSON.
var runStateCodec = enum.Set[RunState]{
	Names:     []string{"running", "done", "abandoned"},
	Invalid:   "invalid-run-state",
	Err:       errRunState,
	ScanLabel: "database value",
}

var (
	errRunClosed     = errors.New("run is missing or already finished")
	errRunTransition = errors.New("invalid run transition")
	errRunState      = errors.New("invalid run state")
)

func (s RunState) String() string { return runStateCodec.String(s) }

// RequireRunning admits work only while the run is active.
func (s RunState) RequireRunning() error {
	if s != RunRunning {
		return errRunClosed
	}
	return nil
}

// transition is the only rule for changing a persisted run state. Storage
// must match the expected state, stage and retry revision atomically and commit
// the returned state with required receipts. Remaining running advances a stage;
// terminal states cannot reopen. A detached caller snapshot is never mutated.
func (s RunState) transition(next RunState) (RunState, error) {
	if err := s.RequireRunning(); err != nil {
		return 0, err
	}
	switch next {
	case RunRunning, RunDone, RunAbandoned:
		return next, nil
	default:
		return 0, fmt.Errorf("%w: %s to %s", errRunTransition, s, next)
	}
}

func ParseRunState(text string) (RunState, error) { return runStateCodec.Parse(text) }

func (s RunState) Value() (driver.Value, error) { return runStateCodec.Value(s) }

func (s *RunState) Scan(value any) error { return runStateCodec.Scan(s, value) }

func (s RunState) MarshalJSON() ([]byte, error) { return runStateCodec.JSON(s) }

func (s *RunState) UnmarshalJSON(data []byte) error { return runStateCodec.ReadJSON(s, data) }
