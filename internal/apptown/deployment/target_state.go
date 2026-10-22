package deployment

import (
	"database/sql/driver"
	"errors"
	"fmt"

	"github.com/flokkit/apptown/internal/enum"
)

// TargetState is a deployment target's lifecycle: answering, not answering,
// or taken out of service. A check moves a target between ready and
// unreachable; retirement is terminal and nothing revives a retired target,
// not a later check and not a configuration refresh.
type TargetState uint8

const (
	TargetReady TargetState = iota + 1
	TargetUnreachable
	TargetRetired
)

// targetStateCodec spells each TargetState in storage and JSON.
var targetStateCodec = enum.Set[TargetState]{
	Names:     []string{"ready", "unreachable", "retired"},
	Invalid:   "invalid-target-state",
	Err:       ErrTargetState,
	ScanLabel: "value",
}

var (
	ErrTargetRetired = errors.New("target is retired")
	ErrTargetState   = errors.New("invalid target state")
)

func (s TargetState) String() string { return targetStateCodec.String(s) }

// Available is the admission rule for binding, offering, editing and checking:
// anything but a retired target.
func (s TargetState) Available() error {
	switch s {
	case TargetReady, TargetUnreachable:
		return nil
	case TargetRetired:
		return ErrTargetRetired
	default:
		return fmt.Errorf("%w: %d", ErrTargetState, s)
	}
}

// Offerable: only a target that answered its last check may take new work.
func (s TargetState) Offerable() bool { return s == TargetReady }

// Checked is the one rule for recording a check's answer.
func (s TargetState) Checked(answered bool) (TargetState, error) {
	if err := s.Available(); err != nil {
		return 0, err
	}
	if answered {
		return TargetReady, nil
	}
	return TargetUnreachable, nil
}

// Retire is the one rule for taking a target out of service. Retiring an
// already retired target is refused with ErrTargetRetired so a caller can
// treat it as already done rather than as a change.
func (s TargetState) Retire() (TargetState, error) {
	if err := s.Available(); err != nil {
		return 0, err
	}
	return TargetRetired, nil
}

func (s TargetState) Value() (driver.Value, error) { return targetStateCodec.Value(s) }

func (s *TargetState) Scan(value any) error { return targetStateCodec.Scan(s, value) }

func (s TargetState) MarshalJSON() ([]byte, error) { return targetStateCodec.JSON(s) }

func (s *TargetState) UnmarshalJSON(data []byte) error {
	return targetStateCodec.ReadJSON(s, data)
}
