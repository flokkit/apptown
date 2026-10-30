package adrs

import (
	"database/sql/driver"
	"errors"
	"github.com/flokkit/apptown/internal/enum"
)

// ADRLabel is a display word, separate from lifecycle and runtime state.
type ADRLabel uint8

const (
	ADRLabelUnknown ADRLabel = iota + 1
	ADRLabelWaiting
	ADRLabelWaitingForYou
	ADRLabelPassed
	ADRLabelFailed
	ADRLabelStopped
)

var adrLabelCodec = enum.Set[ADRLabel]{Names: []string{"unknown", "waiting", "waiting for you", "passed", "failed", "stopped"}, Invalid: "invalid-display-label", Err: errors.New("invalid display label"), ScanLabel: "value"}

func (s ADRLabel) String() string               { return adrLabelCodec.String(s) }
func (s ADRLabel) Value() (driver.Value, error) { return adrLabelCodec.Value(s) }
func (s ADRLabel) MarshalJSON() ([]byte, error) { return adrLabelCodec.JSON(s) }

func (s State) Display() ADRLabel {
	switch s {
	case Draft:
		return ADRLabelWaiting
	case Proposed:
		return ADRLabelWaitingForYou
	case Accepted:
		return ADRLabelPassed
	case Rejected:
		return ADRLabelFailed
	case Deprecated, Superseded:
		return ADRLabelStopped
	default:
		return ADRLabelUnknown
	}
}

func (s *ADRLabel) UnmarshalJSON(b []byte) error { return adrLabelCodec.ReadJSON(s, b) }
