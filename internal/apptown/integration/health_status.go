package integration

import (
	"database/sql/driver"
	"errors"

	"github.com/flokkit/apptown/internal/enum"
)

// HealthStatus labels the check result without classifying provider reachability.
type HealthStatus uint8

const (
	HealthUnknown HealthStatus = iota + 1
	HealthPassed
	HealthFailed
	HealthNotSetUp
)

var healthStatusCodec = enum.Set[HealthStatus]{Names: []string{"unknown", "passed", "failed", "not set up"}, Invalid: "invalid-health-status", Err: errors.New("invalid health status"), ScanLabel: "value"}

func (s HealthStatus) String() string                   { return healthStatusCodec.String(s) }
func (s HealthStatus) Value() (driver.Value, error)     { return healthStatusCodec.Value(s) }
func (s HealthStatus) MarshalJSON() ([]byte, error)     { return healthStatusCodec.JSON(s) }
func (s *HealthStatus) UnmarshalJSON(data []byte) error { return healthStatusCodec.ReadJSON(s, data) }

// ForDisplay supplies the owner's label even when no check exists.
func (h Health) ForDisplay() Health {
	if h.NotSetUp {
		h.StateLabel = HealthNotSetUp
		return h
	}
	// A check can run yet leave credentials unassessed, for example when a
	// registry names a token service that cannot safely receive its secret.
	if h.StateLabel == HealthUnknown {
		return h
	}
	h.StateLabel = HealthUnknown
	if h.Checked {
		if h.OK {
			h.StateLabel = HealthPassed
		} else {
			h.StateLabel = HealthFailed
		}
	}
	return h
}
