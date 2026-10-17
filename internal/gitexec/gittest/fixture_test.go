package gittest

import (
	"os"
	"path/filepath"
	"testing"
)

var twoCommits = []Commit{
	{Message: "first", Files: map[string]string{"a.txt": "a\n", "dir/b.txt": "b\n"}},
	{Message: "second", Files: map[string]string{"a.txt": "changed\n"}},
}

// A fixture is the history it names, checked out with nothing to commit,
// and the same commits make the same ids every time.
func TestARepositoryIsItsCommitsCheckedOutAndRepeatable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "repo")
	ids := Repository(t, dir, "main", twoCommits...)
	if len(ids) != 2 || Run(t, dir, "rev-parse", "HEAD") != ids[1] || Run(t, dir, "rev-parse", "HEAD~1") != ids[0] {
		t.Fatalf("ids %v do not name the history", ids)
	}
	if got := Run(t, dir, "symbolic-ref", "--short", "HEAD"); got != "main" {
		t.Fatalf("on %q", got)
	}
	if got := Run(t, dir, "status", "--porcelain"); got != "" {
		t.Fatalf("status after building:\n%s", got)
	}
	for path, want := range map[string]string{"a.txt": "changed\n", "dir/b.txt": "b\n"} {
		if body, err := os.ReadFile(filepath.Join(dir, path)); err != nil || string(body) != want {
			t.Fatalf("%s is %q (%v), want %q", path, body, err, want)
		}
	}
	if got := Run(t, dir, "log", "-1", "--format=%an <%ae> %cn <%ce>"); got != "test <test@example.com> test <test@example.com>" {
		t.Fatalf("identity %q", got)
	}
	again := BareRepository(t, filepath.Join(t.TempDir(), "bare"), "main", twoCommits...)
	if again[0] != ids[0] || again[1] != ids[1] {
		t.Fatalf("the same commits made %v and %v", ids, again)
	}
}

// A fixture carries its own identity, so a commit a test makes in it later
// with plain git works on a machine with no git identity of its own (a CI
// runner): the forge's git reads no machine-wide config.
func TestAFixtureCommitsAsTheTesterOnAnyMachine(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "repo")
	Repository(t, dir, "main", twoCommits[0])
	Run(t, dir, "commit", "-q", "--allow-empty", "-m", "later")
	if got := Run(t, dir, "log", "-1", "--format=%an <%ae>"); got != "test <test@example.com>" {
		t.Fatalf("a later commit is by %q", got)
	}
	bare := filepath.Join(t.TempDir(), "bare")
	BareRepository(t, bare, "main")
	if got := Run(t, bare, "config", "user.email"); got != "test@example.com" {
		t.Fatalf("the bare fixture's identity is %q", got)
	}
}

// Each copy of a template is its own repository: a commit and a file
// written in one copy reach neither the template nor another copy.
func TestACopyIsItsOwnRepository(t *testing.T) {
	var ids []string
	template := NewTemplate(func(t testing.TB, dir string) { ids = Repository(t, dir, "main", twoCommits...) })
	one, other := template.Copy(t, "one"), template.Copy(t, "other")
	if err := os.WriteFile(filepath.Join(one, "a.txt"), []byte("written in one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	RunAsTester(t, one, "commit", "-q", "-am", "only in one")
	for _, dir := range []string{template.dir, other} {
		if got := Run(t, dir, "rev-parse", "HEAD"); got != ids[1] {
			t.Fatalf("%s moved to %s", dir, got)
		}
		if body, _ := os.ReadFile(filepath.Join(dir, "a.txt")); string(body) != "changed\n" {
			t.Fatalf("%s holds %q", dir, body)
		}
		if got := Run(t, dir, "status", "--porcelain"); got != "" {
			t.Fatalf("%s status:\n%s", dir, got)
		}
	}
}

func TestMain(m *testing.M) {
	code := m.Run()
	RemoveTemplates()
	os.Exit(code)
}

// The plain copy, used where the file system cannot clone, keeps the same
// promise.
func TestAPlainCopyIsItsOwnRepository(t *testing.T) {
	template := filepath.Join(t.TempDir(), "template")
	ids := Repository(t, template, "main", twoCommits...)
	one := filepath.Join(t.TempDir(), "one")
	if err := plainCopy(template, one); err != nil {
		t.Fatal(err)
	}
	RunAsTester(t, one, "commit", "-q", "--allow-empty", "-m", "only in one")
	if got := Run(t, template, "rev-parse", "HEAD"); got != ids[1] {
		t.Fatalf("the template moved to %s", got)
	}
	if got := Run(t, one, "rev-parse", "HEAD~1"); got != ids[1] {
		t.Fatalf("the copy's parent is %s", got)
	}
}
