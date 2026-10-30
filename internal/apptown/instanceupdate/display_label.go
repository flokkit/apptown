package instanceupdate

import (
	"database/sql/driver"
	"errors"
	"github.com/flokkit/apptown/internal/enum"
)

type UpdateLabel uint8

const (
	UpdateUnknown UpdateLabel = iota + 1
	UpdateWaiting
	UpdateNone
	UpdateCurrent
	UpdateAvailable
	UpdateFailed
)

var updateLabelCodec = enum.Set[UpdateLabel]{Names: []string{"unknown", "waiting", "none", "current", "available", "failed"}, Invalid: "invalid-update-label", Err: errors.New("invalid update label"), ScanLabel: "value"}

func (s UpdateLabel) String() string                { return updateLabelCodec.String(s) }
func (s UpdateLabel) Value() (driver.Value, error)  { return updateLabelCodec.Value(s) }
func (s UpdateLabel) MarshalJSON() ([]byte, error)  { return updateLabelCodec.JSON(s) }
func (s *UpdateLabel) UnmarshalJSON(b []byte) error { return updateLabelCodec.ReadJSON(s, b) }
func updateLabel(state string) UpdateLabel {
	switch state {
	case "checking":
		return UpdateWaiting
	case "no-release":
		return UpdateNone
	case "current":
		return UpdateCurrent
	case "available":
		return UpdateAvailable
	case "unavailable":
		return UpdateFailed
	default:
		return UpdateUnknown
	}
}
