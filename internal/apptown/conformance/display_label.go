package conformance

import (
	"database/sql/driver"
	"errors"
	"github.com/flokkit/apptown/internal/enum"
	"time"
)

// CheckLabel keeps a passed check, an exception and missing evidence distinct.
type CheckLabel uint8

const (
	CheckUnknown CheckLabel = iota + 1
	CheckPassed
	CheckFailed
	CheckExcepted
	CheckNotApplicable
)

var checkLabelCodec = enum.Set[CheckLabel]{Names: []string{"unknown", "passed", "failed", "excepted", "not applicable"}, Invalid: "invalid-check-label", Err: errors.New("invalid check label"), ScanLabel: "value"}

func (s CheckLabel) String() string                   { return checkLabelCodec.String(s) }
func (s CheckLabel) Value() (driver.Value, error)     { return checkLabelCodec.Value(s) }
func (s CheckLabel) MarshalJSON() ([]byte, error)     { return checkLabelCodec.JSON(s) }
func (s *CheckLabel) UnmarshalJSON(data []byte) error { return checkLabelCodec.ReadJSON(s, data) }
func (o Outcome) Label() CheckLabel {
	switch o {
	case Conforms:
		return CheckPassed
	case DoesNotConform:
		return CheckFailed
	case Excepted:
		return CheckExcepted
	case NotApplicable:
		return CheckNotApplicable
	default:
		return CheckUnknown
	}
}
func displayLabel(s ObservationState) CheckLabel {
	if outcome, ok := s.(Outcome); ok {
		return outcome.Label()
	}
	return CheckUnknown
}

// Label preserves the difference between findings, exceptions and incomplete checks.
func (s Standing) Label() CheckLabel {
	switch s {
	case StandingDoesNot:
		return CheckFailed
	case StandingConforms:
		return CheckPassed
	case StandingExcepted:
		return CheckExcepted
	default:
		return CheckUnknown
	}
}

// FindingsLabel classifies the accepted scan reports. Unread or stale evidence cannot pass.
func FindingsLabel(actionable, reports int, unread, stale bool) CheckLabel {
	if actionable > 0 {
		return CheckFailed
	}
	if reports == 0 || unread || stale {
		return CheckUnknown
	}
	return CheckPassed
}

// DisplayAt uses the reading's clock, including equality at the expiry boundary.
func (d Disposition) DisplayAt(at time.Time) CheckLabel {
	if !d.Status.valid() {
		return CheckUnknown
	}
	if !d.ActiveAt(at) {
		return CheckFailed
	}
	return CheckExcepted
}
