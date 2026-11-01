// Package status owns the shared display vocabulary. Domain owners classify
// their own states; these words never authorize a transition.
package status

import (
	"database/sql/driver"
	"errors"

	"github.com/flokkit/apptown/internal/enum"
)

// DisplayStatus is a compact presentation fact, never transition authority.
type DisplayStatus uint8

const (
	DisplayUnknown DisplayStatus = iota + 1
	DisplayWorking
	DisplayWaiting
	DisplayFailed
	DisplayStopped
	DisplayLanded
	DisplayFinished
	DisplayWaitingForYou
	DisplayWaitingForChecks
	DisplayPassed
	DisplayExcepted
	DisplayNotApplicable
	DisplayNone
)

var displayStatusCodec = enum.Set[DisplayStatus]{Names: []string{"unknown", "working", "waiting", "failed", "stopped", "landed", "finished", "waiting for you", "waiting for checks", "passed", "excepted", "not applicable", "none"}, Invalid: "invalid-display-status", Err: errors.New("invalid display status"), ScanLabel: "value"}

func (s DisplayStatus) String() string               { return displayStatusCodec.String(s) }
func (s DisplayStatus) Value() (driver.Value, error) { return displayStatusCodec.Value(s) }
func (s DisplayStatus) MarshalJSON() ([]byte, error) { return displayStatusCodec.JSON(s) }

func (s *DisplayStatus) UnmarshalJSON(data []byte) error { return displayStatusCodec.ReadJSON(s, data) }

// MergeDisplay reports a recorded merge independently of deployment evidence.
func MergeDisplay(merged bool) DisplayStatus {
	if merged {
		return DisplayLanded
	}
	return DisplayNone
}
