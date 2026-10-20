package check_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flokkit.io/forge/internal/check"
)

// The evaluator identity changes with the rubric, its files, or the
// packing/template — and is stable otherwise.
func TestLLMEvaluatorIdentity(t *testing.T) {
	t.Parallel()
	c := check.Declaration{Name: "r", Evaluator: "llm", Scope: "tree", Rubric: "plain", Files: []string{"README.md"}}
	base := check.LLMEvaluatorVersion(c)
	if !strings.HasPrefix(base, "llm/"+check.PackingVersion+"/"+check.TemplateHash()+"/") {
		t.Fatalf("identity shape: %s", base)
	}
	if check.LLMEvaluatorVersion(c) != base {
		t.Fatal("identity must be stable for the same check")
	}
	c2 := c
	c2.Rubric = "plainer"
	c3 := c
	c3.Files = []string{"README.md", "docs/*.md"}
	if check.LLMEvaluatorVersion(c2) == base || check.LLMEvaluatorVersion(c3) == base {
		t.Fatal("a changed rubric or file list is a changed evaluator")
	}
	// Packing is deterministic: same tree, same bundle hash; different tree, different hash.
	dir := t.TempDir()
	writeFile(t, dir, "README.md", "one")
	writeFile(t, dir, "b/README.md", "two")
	b1, files := check.PackEvidence(dir, []string{"**/README.md"})
	b2, _ := check.PackEvidence(dir, []string{"**/README.md"})
	if b1 != b2 || len(files) != 2 || files[0] != "README.md" || files[1] != "b/README.md" {
		t.Fatalf("packing must be deterministic and sorted: %v", files)
	}
	writeFile(t, dir, "README.md", "changed")
	if b3, _ := check.PackEvidence(dir, []string{"**/README.md"}); check.BundleHash(b3) == check.BundleHash(b1) {
		t.Fatal("changed evidence must change the bundle hash")
	}
}

func writeFile(t *testing.T, dir, rel, body string) {
	t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A check author's glob means what it says anywhere in the pattern: "**" spans
// any depth at the start, middle or end, and "*" stays within one directory.
func TestCheckFileGlobsSpanDirectoriesOnlyWhereTheySayTo(t *testing.T) {
	cases := []struct {
		pattern, path string
		want          bool
	}{
		{"**/*.go", "main.go", true},
		{"**/*.go", "a/b/main.go", true},
		{"src/**/*.go", "src/a/b/c.go", true},
		{"src/**/*.go", "src/c.go", true},
		{"src/**/*.go", "lib/c.go", false},
		{"docs/**", "docs/a/b.md", true},
		{"docs/*.md", "docs/a/b.md", false},
		{"Dockerfile*", "Dockerfile.prod", true},
		{"[", "[", false},
	}
	for _, c := range cases {
		if got := check.GlobMatch(c.pattern, c.path); got != c.want {
			t.Errorf("globMatch(%q, %q) = %v, want %v", c.pattern, c.path, got, c.want)
		}
	}
}

// A file a check names by its exact path is evidence even inside a directory
// the walk skips: the runtime-contract check names .apptown/app.yaml, and a
// committed contract must not read as missing. AppTown's own runtime state
// beside it stays out, and a symlinked directory never leads outside the tree.
func TestNamedFilesAreEvidenceEvenUnderSkippedDirectories(t *testing.T) {
	t.Parallel()
	globs := []string{"README.md", ".apptown/app.yaml", ".apptown/compose.yaml", "**/*.md", "**/*.yaml"}

	without := t.TempDir()
	writeFile(t, without, "README.md", "an app")
	writeFile(t, without, ".apptown/CHANGE.md", "the implementer's report")
	writeFile(t, without, ".apptown/sessions/s.md", "a transcript")
	if bundle, files := check.PackEvidence(without, globs); len(files) != 1 || files[0] != "README.md" || strings.Contains(bundle, "transcript") || strings.Contains(bundle, "report") {
		t.Fatalf("without a contract only README.md is evidence, got %v\n%s", files, bundle)
	}

	with := t.TempDir()
	writeFile(t, with, "README.md", "an app")
	writeFile(t, with, ".apptown/app.yaml", "app:\n  web: { build: ., public: true }\n")
	writeFile(t, with, ".apptown/CHANGE.md", "the implementer's report")
	bundle, files := check.PackEvidence(with, globs)
	if strings.Join(files, ",") != ".apptown/app.yaml,README.md" || !strings.Contains(bundle, "=== .apptown/app.yaml ===\napp:") {
		t.Fatalf("a committed contract is evidence, got %v\n%s", files, bundle)
	}

	outside := t.TempDir()
	writeFile(t, outside, "app.yaml", "secret: outside the tree")
	linked := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(linked, ".apptown")); err != nil {
		t.Fatal(err)
	}
	if bundle, files := check.PackEvidence(linked, globs); len(files) != 0 || strings.Contains(bundle, "outside") {
		t.Fatalf("a symlinked directory must not lead evidence outside the tree, got %v", files)
	}
}
