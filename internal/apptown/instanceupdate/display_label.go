package instanceupdate

import (
	"database/sql/driver"
	"errors"

	"github.com/flokkit/apptown/internal/enum"
)

type UpdateLabel uint8

const (
	updateUnknown UpdateLabel = iota + 1
	updateWaiting
	updateNone
	updateCurrent
	updateAvailable
	updateFailed
)

var updateLabelCodec = enum.Set[UpdateLabel]{Names: []string{"unknown", "waiting", "none", "current", "available", "failed"}, Invalid: "invalid-update-label", Err: errors.New("invalid update label"), ScanLabel: "value"}

func (s UpdateLabel) String() string                { return updateLabelCodec.String(s) }
func (s UpdateLabel) Value() (driver.Value, error)  { return updateLabelCodec.Value(s) }
func (s UpdateLabel) MarshalJSON() ([]byte, error)  { return updateLabelCodec.JSON(s) }
func (s *UpdateLabel) UnmarshalJSON(b []byte) error { return updateLabelCodec.ReadJSON(s, b) }
func updateLabel(state string) UpdateLabel {
	switch state {
	case "checking":
		return updateWaiting
	case "no-release":
		return updateNone
	case "current":
		return updateCurrent
	case "available":
		return updateAvailable
	case "unavailable":
		return updateFailed
	default:
		return updateUnknown
	}
}
