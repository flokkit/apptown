package attention

import (
	"database/sql/driver"
	"errors"
	"github.com/flokkit/apptown/internal/enum"
)

type AttentionLabel uint8

const (
	LabelUnknown AttentionLabel = 1
	LabelWaiting AttentionLabel = 2
)

var labelCodec = enum.Set[AttentionLabel]{Names: []string{"unknown", "waiting"}, Invalid: "invalid-status-label", Err: errors.New("invalid status label"), ScanLabel: "value"}

func (s AttentionLabel) String() string                { return labelCodec.String(s) }
func (s AttentionLabel) Value() (driver.Value, error)  { return labelCodec.Value(s) }
func (s AttentionLabel) MarshalJSON() ([]byte, error)  { return labelCodec.JSON(s) }
func (s *AttentionLabel) UnmarshalJSON(b []byte) error { return labelCodec.ReadJSON(s, b) }
