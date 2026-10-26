package dataprotection

import (
	"path/filepath"
	"testing"
)

func TestLocalBackupDirectoryPreservesConfiguredPathForm(t *testing.T) {
	for _, c := range []struct{ data, want string }{
		{"state", "state/backups"},
		{"state/../kept", "kept/backups"},
		{"/srv/apptown", "/srv/apptown/backups"},
	} {
		s := &Service{core: &core{dataDir: filepath.FromSlash(c.data)}}
		if got, want := s.BackupDirectory(), filepath.FromSlash(c.want); got != want {
			t.Fatalf("data directory %q: got %q, want %q", c.data, got, want)
		}
	}
}
