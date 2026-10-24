package projectlifecycle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/scm"
)

// A seed's ignore rules merge into an existing .gitignore instead of being
// dropped because the file already exists.
func TestSeedIgnoreRulesMerge(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dst := filepath.Join(dir, ".gitignore")
	if err := os.WriteFile(dst, []byte("# forge\n.apptown/sessions\nnode_modules/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	seed := []byte("# Godot's import cache\n.godot/\n# the export\nbuild/\nnode_modules/\n")
	merge := func() bool {
		changed, err := scm.UpdateInTree(dir, ".gitignore", func(existing []byte) []byte { return mergeIgnoreLines(existing, seed) })
		if err != nil {
			t.Fatal(err)
		}
		return changed
	}
	if !merge() {
		t.Fatal("expected lines to be added")
	}
	b, _ := os.ReadFile(dst)
	s := string(b)
	if !strings.Contains(s, ".godot/") || !strings.Contains(s, "build/") || strings.Count(s, "node_modules/") != 1 || !strings.Contains(s, "# Godot's import cache") {
		t.Fatalf("merged .gitignore wrong:\n%s", s)
	}
	if merge() {
		t.Fatal("a second merge adds nothing")
	}
}
