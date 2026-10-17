package gittest

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// Fixture git through Run and Command leaves the repository the test's
// environment names untouched: with GIT_DIR, GIT_WORK_TREE and
// GIT_INDEX_FILE naming a decoy, a fixture initialised, configured,
// committed and tagged in its own
// folder changes the decoy's HEAD, index, config and refs not at all. Not
// parallel: it sets the process's environment.
func TestFixtureGitLeavesTheEnvironmentsRepositoryAlone(t *testing.T) {
	decoy := t.TempDir()
	RunAsTester(t, decoy, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(decoy, "decoy.txt"), []byte("decoy\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	RunAsTester(t, decoy, "add", "decoy.txt")
	RunAsTester(t, decoy, "commit", "-q", "-m", "decoy")
	// HEAD, the index, the config and every ref.
	snapshot := func() []byte {
		var all []byte
		for _, f := range []string{"HEAD", "index", "config"} {
			b, err := os.ReadFile(filepath.Join(decoy, ".git", f))
			if err != nil {
				t.Fatal(err)
			}
			all = append(append(all, f+":"...), b...)
		}
		if err := filepath.WalkDir(filepath.Join(decoy, ".git", "refs"), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			b, err := os.ReadFile(path)
			all = append(append(all, path+":"...), b...)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return all
	}
	before := snapshot()
	t.Setenv("GIT_DIR", filepath.Join(decoy, ".git"))
	t.Setenv("GIT_WORK_TREE", decoy)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(decoy, ".git", "index"))

	fixture := t.TempDir()
	RunAsTester(t, fixture, "init", "-q", "-b", "work")
	Run(t, fixture, "config", "user.name", "fixture")
	if err := os.WriteFile(filepath.Join(fixture, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	RunAsTester(t, fixture, "add", "a.txt")
	RunAsTester(t, fixture, "commit", "-q", "-m", "fixture")
	if out, err := Command(append(append([]string{"-C", fixture}, Tester...), "commit", "-q", "--allow-empty", "-m", "by Command")...).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if out, err := CommandContext(t.Context(), "-C", fixture, "tag", "made").CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}

	if !bytes.Equal(snapshot(), before) {
		t.Fatal("fixture git changed the repository the environment names")
	}
	if log := Run(t, fixture, "log", "--format=%s"); log != "by Command\nfixture" {
		t.Fatalf("the fixture's own history: %q", log)
	}
}
