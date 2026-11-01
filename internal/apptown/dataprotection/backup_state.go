package dataprotection

import (
	"database/sql/driver"
	"errors"
	"fmt"

	"github.com/flokkit/apptown/internal/enum"
)

// BackupState is a backup set's standing: ok when every item was taken,
// partial when some were, failed when none were, pruned when its files were
// removed under the retention rule. Only a set that holds something can be
// restored or pruned; a failed set is a receipt and nothing more.
type BackupState uint8

const (
	BackupOK BackupState = iota + 1
	BackupPartial
	BackupFailed
	backupPruned
)

// backupStateCodec spells each BackupState in storage and JSON.
var backupStateCodec = enum.Set[BackupState]{
	Names:     []string{"ok", "partial", "failed", "pruned"},
	Invalid:   "invalid-backup-state",
	Err:       errBackupState,
	ScanLabel: "value",
}

var (
	errBackupState      = errors.New("invalid backup state")
	errBackupTransition = errors.New("invalid backup transition")
)

func (s BackupState) String() string { return backupStateCodec.String(s) }

// taken is the rule when a set has been written: what its items say.
func taken(failedItems, totalItems int) BackupState {
	switch {
	case totalItems > 0 && failedItems == totalItems:
		return BackupFailed
	case failedItems > 0:
		return BackupPartial
	default:
		return BackupOK
	}
}

// holds reports whether the set's files still hold data worth restoring.
func (s BackupState) holds() bool { return s == BackupOK || s == BackupPartial }

// restorable is the admission rule for a restore.
func (s BackupState) restorable() error {
	if s.holds() {
		return nil
	}
	return fmt.Errorf("%w: a %s set cannot be restored", errBackupTransition, s)
}

func (s BackupState) Value() (driver.Value, error) { return backupStateCodec.Value(s) }

func (s *BackupState) Scan(value any) error { return backupStateCodec.Scan(s, value) }

func (s BackupState) MarshalJSON() ([]byte, error) { return backupStateCodec.JSON(s) }

func (s *BackupState) UnmarshalJSON(data []byte) error {
	return backupStateCodec.ReadJSON(s, data)
}
