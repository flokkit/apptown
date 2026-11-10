package adrs

import (
	"database/sql/driver"
	"errors"

	"github.com/flokkit/apptown/internal/enum"
)

// ADRLabel is a display word, separate from lifecycle and runtime state.
type ADRLabel uint8

const (
	adrLabelUnknown ADRLabel = iota + 1
	adrLabelWaiting
	adrLabelWaitingForYou
	adrLabelPassed
	adrLabelFailed
	adrLabelStopped
)

var adrLabelCodec = enum.Set[ADRLabel]{Names: []string{"unknown", "waiting", "waiting for you", "passed", "failed", "stopped"}, Invalid: "invalid-display-label", Err: errors.New("invalid display label"), ScanLabel: "value"}

func (s ADRLabel) String() string               { return adrLabelCodec.String(s) }
func (s ADRLabel) Value() (driver.Value, error) { return adrLabelCodec.Value(s) }
func (s ADRLabel) MarshalJSON() ([]byte, error) { return adrLabelCodec.JSON(s) }

func (s State) display() ADRLabel {
	switch s {
	case Draft:
		return adrLabelWaiting
	case proposed:
		return adrLabelWaitingForYou
	case Accepted:
		return adrLabelPassed
	case rejected:
		return adrLabelFailed
	case Deprecated, Superseded:
		return adrLabelStopped
	default:
		return adrLabelUnknown
	}
}

// help explains the display word in the lifecycle's terms, so the word and
// its explanation say one thing ("waiting" is a draft not proposed yet).
func (s State) help() string {
	switch s {
	case Draft:
		return "A draft: waiting for its author to propose it."
	case proposed:
		return "Proposed: waiting for someone to accept or reject it."
	case Accepted:
		return "Accepted: this is the decision."
	case rejected:
		return "Rejected."
	case Deprecated:
		return "Deprecated: no longer followed."
	case Superseded:
		return "Superseded by a later decision."
	default:
		return "Its state could not be read."
	}
}

func (s *ADRLabel) UnmarshalJSON(b []byte) error { return adrLabelCodec.ReadJSON(s, b) }
