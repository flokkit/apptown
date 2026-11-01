package dataprotection

import (
	"database/sql/driver"
	"errors"

	"github.com/flokkit/apptown/internal/enum"
)

type CopyLabel uint8

const (
	copyLabelUnknown CopyLabel = iota + 1
	copyLabelFresh
	copyLabelOverdue
	copyLabelMissing
	copyLabelDisposable
)

var copyLabelCodec = enum.Set[CopyLabel]{Names: []string{"unknown", "fresh", "overdue", "missing", "disposable"}, Invalid: "invalid-copy-label", Err: errors.New("invalid copy label"), ScanLabel: "value"}

func (s CopyLabel) String() string                { return copyLabelCodec.String(s) }
func (s CopyLabel) Value() (driver.Value, error)  { return copyLabelCodec.Value(s) }
func (s CopyLabel) MarshalJSON() ([]byte, error)  { return copyLabelCodec.JSON(s) }
func (s *CopyLabel) UnmarshalJSON(b []byte) error { return copyLabelCodec.ReadJSON(s, b) }

// label describes copy age or absence without asserting host reachability.
func (s CopyState) label() CopyLabel {
	switch s {
	case CopyFresh:
		return copyLabelFresh
	case CopyStale:
		return copyLabelOverdue
	case CopyNone:
		return copyLabelMissing
	case CopyDisposable:
		return copyLabelDisposable
	default:
		return copyLabelUnknown
	}
}
