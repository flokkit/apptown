package projectnotes

import (
	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"os"
	"path/filepath"
	"testing"
)

func TestReadUsesExactCommittedBytes(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	gittest.Run(t, repo, "init", "-b", "main")
	gittest.Run(t, repo, "config", "user.name", "test")
	gittest.Run(t, repo, "config", "user.email", "test@example.test")
	for _, body := range []string{" # Project notes\n", "# Project notes\n- Fact. (agent · change 1) \n", "# Project notes\n- Fact. (agent · change 1)\n"} {
		if err := os.WriteFile(filepath.Join(repo, Path), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
		gittest.Run(t, repo, "add", Path)
		gittest.Run(t, repo, "commit", "-m", "notes")
		_, wantErr := parse([]byte(body))
		_, present, err := Read(t.Context(), repo, "HEAD")
		if !present || (err == nil) != (wantErr == nil) {
			t.Fatalf("%q: present=%v err=%v want=%v", body, present, err, wantErr)
		}
	}
}
