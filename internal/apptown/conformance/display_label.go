package conformance

import (
	"database/sql/driver"
	"errors"
	"time"

	"github.com/flokkit/apptown/internal/enum"
)

// CheckLabel keeps a passed check, an exception and missing evidence distinct.
type CheckLabel uint8

const (
	checkUnknown CheckLabel = iota + 1
	checkPassed
	checkFailed
	checkExcepted
	checkNotApplicable
)

var checkLabelCodec = enum.Set[CheckLabel]{Names: []string{"unknown", "passed", "failed", "excepted", "not applicable"}, Invalid: "invalid-check-label", Err: errors.New("invalid check label"), ScanLabel: "value"}

func (s CheckLabel) String() string                   { return checkLabelCodec.String(s) }
func (s CheckLabel) Value() (driver.Value, error)     { return checkLabelCodec.Value(s) }
func (s CheckLabel) MarshalJSON() ([]byte, error)     { return checkLabelCodec.JSON(s) }
func (s *CheckLabel) UnmarshalJSON(data []byte) error { return checkLabelCodec.ReadJSON(s, data) }
func (o Outcome) label() CheckLabel {
	switch o {
	case Conforms:
		return checkPassed
	case DoesNotConform:
		return checkFailed
	case Excepted:
		return checkExcepted
	case NotApplicable:
		return checkNotApplicable
	default:
		return checkUnknown
	}
}
func displayLabel(s ObservationState) CheckLabel {
	if outcome, ok := s.(Outcome); ok {
		return outcome.label()
	}
	return checkUnknown
}

// label preserves the difference between findings, exceptions and incomplete checks.
func (s Standing) label() CheckLabel {
	switch s {
	case standingDoesNot:
		return checkFailed
	case StandingConforms:
		return checkPassed
	case standingExcepted:
		return checkExcepted
	default:
		return checkUnknown
	}
}

// FindingsLabel classifies the accepted scan reports. Unread or stale evidence cannot pass.
func FindingsLabel(actionable, reports int, unread, stale bool) CheckLabel {
	if actionable > 0 {
		return checkFailed
	}
	if reports == 0 || unread || stale {
		return checkUnknown
	}
	return checkPassed
}

// DisplayAt uses the reading's clock, including equality at the expiry boundary.
func (d Disposition) DisplayAt(at time.Time) CheckLabel {
	if !d.Status.valid() {
		return checkUnknown
	}
	if !d.activeAt(at) {
		return checkFailed
	}
	return checkExcepted
}
