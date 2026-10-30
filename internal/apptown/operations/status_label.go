package operations

import (
	"database/sql/driver"
	"errors"
	"github.com/flokkit/apptown/internal/enum"
)

type OperationLabel uint8

const (
	LabelUnknown  OperationLabel = 1
	LabelWaiting  OperationLabel = 2
	LabelWorking  OperationLabel = 3
	LabelFinished OperationLabel = 4
	LabelFailed   OperationLabel = 5
	LabelStopped  OperationLabel = 6
)

var labelCodec = enum.Set[OperationLabel]{Names: []string{"unknown", "waiting", "working", "finished", "failed", "stopped"}, Invalid: "invalid-status-label", Err: errors.New("invalid status label"), ScanLabel: "value"}

func (s OperationLabel) String() string                { return labelCodec.String(s) }
func (s OperationLabel) Value() (driver.Value, error)  { return labelCodec.Value(s) }
func (s OperationLabel) MarshalJSON() ([]byte, error)  { return labelCodec.JSON(s) }
func (s *OperationLabel) UnmarshalJSON(b []byte) error { return labelCodec.ReadJSON(s, b) }

func (o Operation) label() OperationLabel {
	switch o.State {
	case Queued:
		return LabelWaiting
	case Running:
		return LabelWorking
	case Succeeded:
		return LabelFinished
	case Failed:
		return LabelFailed
	case Cancelled:
		return LabelStopped
	default:
		return LabelUnknown
	}
}
