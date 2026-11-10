package dataprotection

import (
	"database/sql/driver"
	"errors"

	"github.com/flokkit/apptown/internal/enum"
)

type BackupLabel uint8

const (
	labelUnknown  BackupLabel = 1
	labelFinished BackupLabel = 2
	labelFailed   BackupLabel = 3
	labelStopped  BackupLabel = 4
	// labelRemoved is an old set retention has pruned: its record stays, its
	// copy is gone, so it is neither a finished copy nor a failure.
	labelRemoved BackupLabel = 5
)

var labelCodec = enum.Set[BackupLabel]{Names: []string{"unknown", "finished", "failed", "stopped", "removed"}, Invalid: "invalid-status-label", Err: errors.New("invalid status label"), ScanLabel: "value"}

func (s BackupLabel) String() string                { return labelCodec.String(s) }
func (s BackupLabel) Value() (driver.Value, error)  { return labelCodec.Value(s) }
func (s BackupLabel) MarshalJSON() ([]byte, error)  { return labelCodec.JSON(s) }
func (s *BackupLabel) UnmarshalJSON(b []byte) error { return labelCodec.ReadJSON(s, b) }

func (b Backup) label() BackupLabel {
	switch b.Status {
	case BackupOK:
		return labelFinished
	case backupPruned:
		return labelRemoved
	case BackupPartial, BackupFailed:
		return labelFailed
	default:
		return labelUnknown
	}
}
