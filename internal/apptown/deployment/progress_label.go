package deployment

import (
	"database/sql/driver"
	"errors"

	"github.com/flokkit/apptown/internal/enum"
)

// ProgressLabel is a display word, separate from lifecycle and runtime state.
type ProgressLabel uint8

const (
	progressLabelUnknown ProgressLabel = iota + 1
	progressLabelDeployed
	progressLabelDeploying
	progressLabelFailed
	progressLabelPassed
	progressLabelWorking
	progressLabelStopped
	progressLabelWaiting
	progressLabelExcepted
)

var progressLabelCodec = enum.Set[ProgressLabel]{Names: []string{"unknown", "deployed", "deploying", "failed", "passed", "working", "stopped", "waiting", "excepted"}, Invalid: "invalid-display-label", Err: errors.New("invalid display label"), ScanLabel: "value"}

func (s ProgressLabel) String() string               { return progressLabelCodec.String(s) }
func (s ProgressLabel) Value() (driver.Value, error) { return progressLabelCodec.Value(s) }
func (s ProgressLabel) MarshalJSON() ([]byte, error) { return progressLabelCodec.JSON(s) }

func (s Readiness) Display() ProgressLabel {
	switch s {
	case Reconciled:
		return progressLabelDeployed
	case reconciling:
		return progressLabelDeploying
	case ReadinessFailed:
		return progressLabelFailed
	default:
		return progressLabelUnknown
	}
}
func (s RestoreCheck) Display() ProgressLabel {
	switch s {
	case restoreGone, RestoreNotFilled:
		return progressLabelFailed
	case RestoreFilling:
		return progressLabelWorking
	case RestoreUnconfirmed:
		return progressLabelWaiting
	default:
		return progressLabelUnknown
	}
}
func (s needState) Display() ProgressLabel {
	switch s {
	case NeedReady:
		return progressLabelPassed
	case needCreating:
		return progressLabelWorking
	case needFailed:
		return progressLabelFailed
	case needOrphaned:
		return progressLabelStopped
	default:
		return progressLabelUnknown
	}
}

func (s *ProgressLabel) UnmarshalJSON(b []byte) error { return progressLabelCodec.ReadJSON(s, b) }

// overrideDisplay distinguishes a requested override from one the generation applies.
func overrideDisplay(applied bool) ProgressLabel {
	if applied {
		return progressLabelExcepted
	}
	return progressLabelWaiting
}
