package operations

import (
	"database/sql/driver"
	"errors"

	"github.com/flokkit/apptown/internal/enum"
)

type OperationLabel uint8

const (
	labelUnknown  OperationLabel = 1
	labelWaiting  OperationLabel = 2
	labelWorking  OperationLabel = 3
	labelFinished OperationLabel = 4
	labelFailed   OperationLabel = 5
	labelStopped  OperationLabel = 6
)

var labelCodec = enum.Set[OperationLabel]{Names: []string{"unknown", "waiting", "working", "finished", "failed", "stopped"}, Invalid: "invalid-status-label", Err: errors.New("invalid status label"), ScanLabel: "value"}

func (s OperationLabel) String() string                { return labelCodec.String(s) }
func (s OperationLabel) Value() (driver.Value, error)  { return labelCodec.Value(s) }
func (s OperationLabel) MarshalJSON() ([]byte, error)  { return labelCodec.JSON(s) }
func (s *OperationLabel) UnmarshalJSON(b []byte) error { return labelCodec.ReadJSON(s, b) }

func (o Operation) label() OperationLabel {
	switch o.State {
	case Queued:
		return labelWaiting
	case Running:
		return labelWorking
	case Succeeded:
		return labelFinished
	case Failed:
		return labelFailed
	case Cancelled:
		return labelStopped
	default:
		return labelUnknown
	}
}
