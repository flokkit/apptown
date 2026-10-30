package dataprotection

import (
	"database/sql/driver"
	"errors"
	"github.com/flokkit/apptown/internal/enum"
)

type BackupLabel uint8

const (
	LabelUnknown  BackupLabel = 1
	LabelFinished BackupLabel = 2
	LabelFailed   BackupLabel = 3
	LabelStopped  BackupLabel = 4
)

var labelCodec = enum.Set[BackupLabel]{Names: []string{"unknown", "finished", "failed", "stopped"}, Invalid: "invalid-status-label", Err: errors.New("invalid status label"), ScanLabel: "value"}

func (s BackupLabel) String() string                { return labelCodec.String(s) }
func (s BackupLabel) Value() (driver.Value, error)  { return labelCodec.Value(s) }
func (s BackupLabel) MarshalJSON() ([]byte, error)  { return labelCodec.JSON(s) }
func (s *BackupLabel) UnmarshalJSON(b []byte) error { return labelCodec.ReadJSON(s, b) }

func (b Backup) label() BackupLabel {
	switch b.Status {
	case BackupOK:
		return LabelFinished
	case BackupPartial, BackupFailed:
		return LabelFailed
	case BackupPruned:
		return LabelStopped
	default:
		return LabelUnknown
	}
}
