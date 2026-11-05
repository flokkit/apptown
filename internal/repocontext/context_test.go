package repocontext

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/gitexec"
)

// The initial brief is bound to the selected commit, independent of subsequent
// main commits, uncommitted edits, and any model-directed file read.
func TestBriefUsesSelectedCommitAndRecordsTruncation(t *testing.T) {
	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		out, err := gitexec.Run(t.Context(), repo, args...)
		if err != nil {
			t.Fatalf("git %v: %s %v", args, out, err)
		}
		return out
	}
	git("init", "-b", "main")
	git("config", "user.email", "test@example.test")
	git("config", "user.name", "Test")
	git("commit", "--allow-empty", "-m", "empty")
	empty := git("rev-parse", "HEAD")
	file := filepath.Join(repo, "PROJECT-NOTES.md")
	if err := os.WriteFile(file, []byte("distinctive accepted preference"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-m", "notes")
	selected := git("rev-parse", "HEAD")
	if err := os.WriteFile(file, []byte("unaccepted replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		commit string
		cap    int
		want   string
	}{{empty, 6144, ""}, {selected, 6144, "distinctive accepted preference"}, {selected, 11, "distinctive"}} {
		brief, err := Brief(t.Context(), repo, tc.commit, []File{{Path: "PROJECT-NOTES.md", MaxBytes: tc.cap}})
		if err != nil {
			t.Fatal(err)
		}
		if tc.want == "" {
			if brief != "" {
				t.Fatalf("absent: %s", brief)
			}
			continue
		}
		if !strings.Contains(brief, tc.want) || !strings.Contains(brief, tc.commit) || strings.Contains(brief, "unaccepted replacement") {
			t.Fatalf("wrong input: %s", brief)
		}
		if (tc.cap == 11) != strings.Contains(brief, "[truncated:") {
			t.Fatalf("truncation: %s", brief)
		}
	}
}
