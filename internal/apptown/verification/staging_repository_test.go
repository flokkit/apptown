package verification

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStagingAPinnedRevisionNeedsARepository(t *testing.T) {
	data := t.TempDir()
	ws := filepath.Join(data, "workspaces", "change-00000000-0001-7000-8000-000000000000")
	if err := os.MkdirAll(ws, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "app.txt"), []byte("unchecked live edits"), 0644); err != nil {
		t.Fatal(err)
	}
	staged, cleanup, err := StageCommittedTree(t.Context(), data, ws, strings.Repeat("1", 40))
	defer cleanup()
	if err == nil {
		t.Fatalf("unavailable pinned commit was accepted as %q (live workspace %q)", staged, ws)
	}
}
