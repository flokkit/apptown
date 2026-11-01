package dataprotection

import (
	"errors"
	"testing"
)

func TestBackupStateFollowsItsItems(t *testing.T) {
	t.Parallel()
	if taken(0, 3) != BackupOK || taken(1, 3) != BackupPartial || taken(3, 3) != BackupFailed || taken(0, 0) != BackupOK {
		t.Fatal("a set is ok, partial or failed by what its items say")
	}
	for _, s := range []BackupState{BackupOK, BackupPartial} {
		if err := s.restorable(); err != nil || !s.holds() {
			t.Fatalf("%s holds data and restores: %v", s, err)
		}

	}
	for _, s := range []BackupState{BackupFailed, backupPruned} {
		if err := s.restorable(); !errors.Is(err, errBackupTransition) || s.holds() {
			t.Fatalf("%s cannot be restored: %v", s, err)
		}
	}

}

func TestBackupStateStorage(t *testing.T) {
	t.Parallel()
	var zero BackupState
	if _, err := zero.Value(); !errors.Is(err, errBackupState) {
		t.Fatalf("the zero value cannot be stored: %v", err)
	}
	var s BackupState
	for _, word := range []string{"ok", "partial", "failed", "pruned"} {
		if err := s.Scan(word); err != nil || s.String() != word {
			t.Fatalf("scan %q: %s %v", word, s, err)
		}
	}
	if err := s.Scan("gone"); !errors.Is(err, errBackupState) {
		t.Fatalf("an unknown stored word is refused: %v", err)
	}
}
