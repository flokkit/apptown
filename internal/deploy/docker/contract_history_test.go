package docker

import (
	"os"
	"path/filepath"
	"testing"

	"flokkit.io/forge/internal/gitexec/gittest"
)

func writeContractAt(t *testing.T, root, rel, image string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "app:\n  web:\n    image: " + image + "\n    public: true\n    ports: [\"8080\"]\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A revision recorded before the AppTown rename holds its contract at
// .forge/app.yaml, and history does not change: the contract at that
// revision is read from there. A revision with .apptown/app.yaml is read
// from the new path, whatever else it holds.
func TestTheContractAtARevisionFallsBackToTheFolderBeforeTheRename(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	gittest.RunAsTester(t, repo, "init", "-q", "-b", "main")
	writeContractAt(t, repo, HistoricalContractPath, "old:v1")
	gittest.RunAsTester(t, repo, "add", "-f", "-A")
	gittest.RunAsTester(t, repo, "commit", "-qm", "before")
	before := gittest.RunAsTester(t, repo, "rev-parse", "HEAD")
	writeContractAt(t, repo, ContractPath, "new:v1")
	gittest.RunAsTester(t, repo, "add", "-f", "-A")
	gittest.RunAsTester(t, repo, "commit", "-qm", "after")
	after := gittest.RunAsTester(t, repo, "rev-parse", "HEAD")

	for sha, want := range map[string]string{before: HistoricalContractPath, after: ContractPath} {
		raw, path, err := ContractFileAt(t.Context(), repo, sha)
		if err != nil || path != want {
			t.Fatalf("contract at %s read from %q (%v), want %q", sha, path, err, want)
		}
		if len(raw) == 0 {
			t.Fatalf("contract at %s is empty", sha)
		}
	}

	folder := t.TempDir()
	writeContractAt(t, folder, HistoricalContractPath, "old:v1")
	c, err := ReadSelectedContract(folder)
	if err != nil || c.PublicPart().Image != "old:v1" {
		t.Fatalf("a folder with only the old contract: %+v %v", c, err)
	}
	writeContractAt(t, folder, ContractPath, "new:v1")
	if c, err = ReadSelectedContract(folder); err != nil || c.PublicPart().Image != "new:v1" {
		t.Fatalf("a folder with both: %+v %v, want the new path read", c, err)
	}
}
