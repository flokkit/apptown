package dataprotection

import (
	"errors"
	"testing"
)

func TestBackupStateFollowsItsItems(t *testing.T) {
	t.Parallel()
	if Taken(0, 3) != BackupOK || Taken(1, 3) != BackupPartial || Taken(3, 3) != BackupFailed || Taken(0, 0) != BackupOK {
		t.Fatal("a set is ok, partial or failed by what its items say")
	}
	for _, s := range []BackupState{BackupOK, BackupPartial} {
		if err := s.Restorable(); err != nil || !s.Holds() {
			t.Fatalf("%s holds data and restores: %v", s, err)
		}
		if got, err := s.Pruned(); err != nil || got != BackupPruned {
			t.Fatalf("%s prunes: %s %v", s, got, err)
		}
	}
	for _, s := range []BackupState{BackupFailed, BackupPruned} {
		if err := s.Restorable(); !errors.Is(err, ErrBackupTransition) || s.Holds() {
			t.Fatalf("%s cannot be restored: %v", s, err)
		}
	}
	if got, err := BackupPruned.Pruned(); err != nil || got != BackupPruned {
		t.Fatalf("recording a prune twice is harmless: %s %v", got, err)
	}
	if _, err := BackupFailed.Pruned(); !errors.Is(err, ErrBackupTransition) {
		t.Fatalf("a failed set has nothing to prune: %v", err)
	}
}

func TestBackupStateStorage(t *testing.T) {
	t.Parallel()
	var zero BackupState
	if _, err := zero.Value(); !errors.Is(err, ErrBackupState) {
		t.Fatalf("the zero value cannot be stored: %v", err)
	}
	var s BackupState
	for _, word := range []string{"ok", "partial", "failed", "pruned"} {
		if err := s.Scan(word); err != nil || s.String() != word {
			t.Fatalf("scan %q: %s %v", word, s, err)
		}
	}
	if err := s.Scan("gone"); !errors.Is(err, ErrBackupState) {
		t.Fatalf("an unknown stored word is refused: %v", err)
	}
}
