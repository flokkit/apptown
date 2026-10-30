package dataprotection

import (
	"database/sql/driver"
	"errors"
	"github.com/flokkit/apptown/internal/enum"
)

type CopyLabel uint8

const (
	CopyLabelUnknown CopyLabel = iota + 1
	CopyLabelFresh
	CopyLabelOverdue
	CopyLabelMissing
	CopyLabelDisposable
)

var copyLabelCodec = enum.Set[CopyLabel]{Names: []string{"unknown", "fresh", "overdue", "missing", "disposable"}, Invalid: "invalid-copy-label", Err: errors.New("invalid copy label"), ScanLabel: "value"}

func (s CopyLabel) String() string                { return copyLabelCodec.String(s) }
func (s CopyLabel) Value() (driver.Value, error)  { return copyLabelCodec.Value(s) }
func (s CopyLabel) MarshalJSON() ([]byte, error)  { return copyLabelCodec.JSON(s) }
func (s *CopyLabel) UnmarshalJSON(b []byte) error { return copyLabelCodec.ReadJSON(s, b) }

// Label describes copy age or absence without asserting host reachability.
func (s CopyState) Label() CopyLabel {
	switch s {
	case CopyFresh:
		return CopyLabelFresh
	case CopyStale:
		return CopyLabelOverdue
	case CopyNone:
		return CopyLabelMissing
	case CopyDisposable:
		return CopyLabelDisposable
	default:
		return CopyLabelUnknown
	}
}
