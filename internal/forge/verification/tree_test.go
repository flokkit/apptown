package verification

import (
	"path/filepath"
	"strings"
	"testing"

	"flokkit.io/forge/internal/gitexec/gittest"
)

// A check stages the commit it checks outside the writable workspace, and a
// commit it cannot stage is an error, never a check of the live folder.
func TestStagingChecksTheCommitOrFails(t *testing.T) {
	dataDir := t.TempDir()
	ws := filepath.Join(dataDir, "workspaces", "change-00000000-0001-7000-8000-000000000000")
	for _, args := range [][]string{{"init", "-q", ws}, {"-C", ws, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "one"}} {
		if out, err := gittest.Command(args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	staged, cleanup, err := StageCommittedTree(t.Context(), dataDir, ws, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if strings.HasPrefix(staged, ws) || !strings.HasPrefix(staged, filepath.Join(dataDir, "staging")) {
		t.Fatalf("staged at %s, want under the data directory's staging, outside the workspace", staged)
	}
	if staged, _, err := StageCommittedTree(t.Context(), dataDir, ws, strings.Repeat("0", 40)); err == nil {
		t.Fatalf("an unreadable commit was staged as %s instead of failing", staged)
	}
}
