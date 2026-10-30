package deployment

import (
	"database/sql/driver"
	"errors"
	"github.com/flokkit/apptown/internal/enum"
)

// ProgressLabel is a display word, separate from lifecycle and runtime state.
type ProgressLabel uint8

const (
	ProgressLabelUnknown ProgressLabel = iota + 1
	ProgressLabelDeployed
	ProgressLabelDeploying
	ProgressLabelFailed
	ProgressLabelPassed
	ProgressLabelWorking
	ProgressLabelStopped
	ProgressLabelWaiting
	ProgressLabelExcepted
)

var progressLabelCodec = enum.Set[ProgressLabel]{Names: []string{"unknown", "deployed", "deploying", "failed", "passed", "working", "stopped", "waiting", "excepted"}, Invalid: "invalid-display-label", Err: errors.New("invalid display label"), ScanLabel: "value"}

func (s ProgressLabel) String() string               { return progressLabelCodec.String(s) }
func (s ProgressLabel) Value() (driver.Value, error) { return progressLabelCodec.Value(s) }
func (s ProgressLabel) MarshalJSON() ([]byte, error) { return progressLabelCodec.JSON(s) }

func (s Readiness) Display() ProgressLabel {
	switch s {
	case Reconciled:
		return ProgressLabelDeployed
	case Reconciling:
		return ProgressLabelDeploying
	case ReadinessFailed:
		return ProgressLabelFailed
	default:
		return ProgressLabelUnknown
	}
}
func (s RestoreCheck) Display() ProgressLabel {
	switch s {
	case RestoreGone, RestoreNotFilled:
		return ProgressLabelFailed
	case RestoreFilling:
		return ProgressLabelWorking
	case RestoreUnconfirmed:
		return ProgressLabelWaiting
	default:
		return ProgressLabelUnknown
	}
}
func (s NeedState) Display() ProgressLabel {
	switch s {
	case NeedReady:
		return ProgressLabelPassed
	case NeedCreating:
		return ProgressLabelWorking
	case NeedFailed:
		return ProgressLabelFailed
	case NeedOrphaned:
		return ProgressLabelStopped
	default:
		return ProgressLabelUnknown
	}
}

func (s *ProgressLabel) UnmarshalJSON(b []byte) error { return progressLabelCodec.ReadJSON(s, b) }

// OverrideDisplay distinguishes a requested override from one the generation applies.
func OverrideDisplay(applied bool) ProgressLabel {
	if applied {
		return ProgressLabelExcepted
	}
	return ProgressLabelWaiting
}
