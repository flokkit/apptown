package dataprotection

import (
	"database/sql/driver"
	"errors"
	"fmt"

	"flokkit.io/forge/internal/enum"
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
	BackupPruned
)

// backupStateCodec spells each BackupState in storage and JSON.
var backupStateCodec = enum.Set[BackupState]{
	Names:     []string{"ok", "partial", "failed", "pruned"},
	Invalid:   "invalid-backup-state",
	Err:       ErrBackupState,
	ScanLabel: "value",
}

var (
	ErrBackupState      = errors.New("invalid backup state")
	ErrBackupTransition = errors.New("invalid backup transition")
)

func (s BackupState) String() string { return backupStateCodec.String(s) }

// Taken is the rule when a set has been written: what its items say.
func Taken(failedItems, totalItems int) BackupState {
	switch {
	case totalItems > 0 && failedItems == totalItems:
		return BackupFailed
	case failedItems > 0:
		return BackupPartial
	default:
		return BackupOK
	}
}

// Holds reports whether the set's files still hold data worth restoring.
func (s BackupState) Holds() bool { return s == BackupOK || s == BackupPartial }

// Restorable is the admission rule for a restore.
func (s BackupState) Restorable() error {
	if s.Holds() {
		return nil
	}
	return fmt.Errorf("%w: a %s set cannot be restored", ErrBackupTransition, s)
}

// Pruned is the rule when the retention sweep removed the set's files. A set
// already pruned stays pruned, so recording the removal again is harmless; a
// failed set has no files and is never pruned.
func (s BackupState) Pruned() (BackupState, error) {
	switch s {
	case BackupOK, BackupPartial, BackupPruned:
		return BackupPruned, nil
	default:
		return 0, fmt.Errorf("%w: a %s set has nothing to prune", ErrBackupTransition, s)
	}
}

func (s BackupState) Value() (driver.Value, error) { return backupStateCodec.Value(s) }

func (s *BackupState) Scan(value any) error { return backupStateCodec.Scan(s, value) }

func (s BackupState) MarshalJSON() ([]byte, error) { return backupStateCodec.JSON(s) }

func (s *BackupState) UnmarshalJSON(data []byte) error {
	return backupStateCodec.ReadJSON(s, data)
}
