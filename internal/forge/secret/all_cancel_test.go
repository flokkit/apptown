package secret

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// All cannot say "unknown": a caller that stopped still gets the configured
// values, never an empty map read as "no secrets".
//
// Kills: the read on the stopped caller.
func TestAStoppedCallerStillReadsTheSecrets(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "secrets.env"), []byte("GITLAB_TOKEN=configured\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewService(nil, Config{DataDir: dir})
	stopped, stop := context.WithCancel(t.Context())
	stop()
	if got := s.All(stopped)["GITLAB_TOKEN"]; got != "configured" {
		t.Fatalf("a stopped caller read %q", got)
	}
}
