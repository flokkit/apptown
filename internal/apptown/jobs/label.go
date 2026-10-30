package jobs

import (
	"database/sql/driver"
	"errors"
	"github.com/flokkit/apptown/internal/enum"
)

// JobLabel describes queue progress without implying runtime health.
type JobLabel uint8

const (
	Unknown JobLabel = iota + 1
	Waiting
	Working
	Finished
	Failed
	Stopped
)

var labelCodec = enum.Set[JobLabel]{Names: []string{"unknown", "waiting", "working", "finished", "failed", "stopped"}, Invalid: "invalid-job-label", Err: errors.New("invalid job label"), ScanLabel: "value"}

func (s JobLabel) String() string                { return labelCodec.String(s) }
func (s JobLabel) Value() (driver.Value, error)  { return labelCodec.Value(s) }
func (s JobLabel) MarshalJSON() ([]byte, error)  { return labelCodec.JSON(s) }
func (s *JobLabel) UnmarshalJSON(b []byte) error { return labelCodec.ReadJSON(s, b) }
func stateLabel(s string) JobLabel {
	switch s {
	case "available", "retryable", "scheduled", "pending":
		return Waiting
	case "running":
		return Working
	case "completed":
		return Finished
	case "discarded":
		return Failed
	case "cancelled":
		return Stopped
	default:
		return Unknown
	}
}
