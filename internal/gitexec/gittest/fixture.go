package gittest

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/flokkit/apptown/internal/gitexec"
)

// Commit is one commit of a fixture repository: its message and the files it
// writes (path to content; a file it does not name keeps its earlier
// content).
type Commit struct {
	Message string
	Files   map[string]string
}

// fixtureEpoch is the author and committer time of a fixture's first commit;
// each later commit is a second after the one before. With the Tester
// identity, the same commits make the same commit ids on every run.
const fixtureEpoch = 1700000000

// Repository makes dir a repository on branch whose history is commits, one
// after another, checked out, with the Tester identity in its own config:
// git init, one git fast-import for every commit, and one read-tree for the
// work tree and index, instead of an add and a commit per step. It returns
// the commit ids in order.
func Repository(t testing.TB, dir, branch string, commits ...Commit) []string {
	t.Helper()
	ids := build(t, dir, branch, false, commits)
	if len(commits) > 0 {
		Run(t, dir, "read-tree", "-u", "--reset", "HEAD")
	}
	return ids
}

// BareRepository is Repository without a work tree: git init --bare, the
// Tester identity and one git fast-import.
func BareRepository(t testing.TB, dir, branch string, commits ...Commit) []string {
	t.Helper()
	return build(t, dir, branch, true, commits)
}

func build(t testing.TB, dir, branch string, bare bool, commits []Commit) []string {
	t.Helper()
	if dir == "" {
		t.Fatal("gittest needs a directory for the repository")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	init := []string{"init", "-q", "-b", branch}
	if bare {
		init = append(init, "--bare")
	}
	Run(t, dir, init...)
	ConfigureTester(t, dir)
	if len(commits) == 0 {
		return nil
	}
	var in bytes.Buffer
	for i, c := range commits {
		when := fmt.Sprintf("%d +0000", fixtureEpoch+i)
		fmt.Fprintf(&in, "commit refs/heads/%s\nmark :%d\nauthor test <test@example.com> %s\ncommitter test <test@example.com> %s\ndata %d\n%s\n",
			branch, i+1, when, when, len(c.Message), c.Message)
		paths := make([]string, 0, len(c.Files))
		for p := range c.Files {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		for _, p := range paths {
			fmt.Fprintf(&in, "M 100644 inline %s\ndata %d\n%s\n", p, len(c.Files[p]), c.Files[p])
		}
	}
	marks := filepath.Join(t.TempDir(), "marks")
	if out, err := gitexec.OutputBytes(context.Background(), dir, &in, "fast-import", "--quiet", "--export-marks="+marks); err != nil {
		t.Fatalf("git fast-import in %s: %v: %s", dir, err, out)
	}
	return readMarks(t, marks, len(commits))
}

// testerIdentity is the Tester identity as the repository's own config
// holds it.
const testerIdentity = "[user]\n\tname = test\n\temail = test@example.com\n"

// ConfigureTester gives the repository at dir (a work tree or a bare
// repository) the Tester identity in its own config, written as a file
// rather than by two more git runs. AppTown's git reads no machine-wide
// config, so a repository a test makes with git init or git clone needs
// this before a commit or merge in it works on every machine (a CI runner
// has no identity of its own); Repository and BareRepository do it.
func ConfigureTester(t testing.TB, dir string) {
	t.Helper()
	config := filepath.Join(dir, ".git", "config")
	if _, err := os.Stat(config); err != nil {
		config = filepath.Join(dir, "config")
	}
	f, err := os.OpenFile(config, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(testerIdentity); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func readMarks(t testing.TB, path string, n int) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	ids := make([]string, n)
	s := bufio.NewScanner(f)
	for s.Scan() {
		var mark int
		var id string
		if _, err := fmt.Sscanf(s.Text(), ":%d %s", &mark, &id); err != nil || mark < 1 || mark > n {
			t.Fatalf("fast-import mark %q", s.Text())
		}
		ids[mark-1] = id
	}
	if err := s.Err(); err != nil {
		t.Fatal(err)
	}
	for i, id := range ids {
		if id == "" {
			t.Fatalf("fast-import named no commit for step %d", i+1)
		}
	}
	return ids
}

// Template is a repository built once per test binary, the first time a
// test asks for it, and copied for each test that does: a test gets its own
// repository without starting a process (an APFS clone on darwin, a plain
// copy elsewhere). Every file is the copy's own, so what one test writes
// never reaches the template or another test's copy. A package that uses
// templates calls RemoveTemplates from its TestMain.
type Template struct {
	build func(t testing.TB, dir string)
	once  sync.Once
	dir   string
	built bool
}

// NewTemplate is a template whose repository build makes in dir, an empty
// folder. build must not depend on the test that happens to ask first.
func NewTemplate(build func(t testing.TB, dir string)) *Template {
	return &Template{build: build}
}

var templates struct {
	sync.Mutex
	root string
}

// RemoveTemplates removes every template this test binary built.
func RemoveTemplates() {
	templates.Lock()
	defer templates.Unlock()
	if templates.root != "" {
		_ = os.RemoveAll(templates.root)
		templates.root = ""
	}
}

func templateFolder() (string, error) {
	templates.Lock()
	defer templates.Unlock()
	if templates.root == "" {
		root, err := os.MkdirTemp("", "gittest-templates-")
		if err != nil {
			return "", err
		}
		templates.root = root
	}
	return os.MkdirTemp(templates.root, "")
}

// Copy gives the test its own copy of the template, in a new folder of its
// own named name, and returns that folder.
func (tp *Template) Copy(t testing.TB, name string) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), name)
	tp.CopyTo(t, dst)
	return dst
}

// CopyTo makes dst, which must not exist yet, the test's own copy of the
// template.
func (tp *Template) CopyTo(t testing.TB, dst string) {
	t.Helper()
	tp.once.Do(func() {
		dir, err := templateFolder()
		if err != nil {
			t.Fatal(err)
		}
		tp.dir = filepath.Join(dir, "repo")
		if err := os.Mkdir(tp.dir, 0o755); err != nil {
			t.Fatal(err)
		}
		tp.build(t, tp.dir)
		tp.built = true
	})
	if !tp.built {
		t.Fatal("the template repository did not build (see the first test that asked for it)")
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := copyTree(tp.dir, dst); err != nil {
		t.Fatalf("copy the template to %s: %v", dst, err)
	}
}

// plainCopy copies the tree at src to dst, keeping file modes and symbolic
// links.
func plainCopy(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		to := filepath.Join(dst, rel)
		switch {
		case info.IsDir():
			return os.MkdirAll(to, info.Mode().Perm()|0o700)
		case info.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, to)
		case !info.Mode().IsRegular():
			return fmt.Errorf("%s is neither a file, a folder nor a link", strings.TrimPrefix(path, src))
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.WriteFile(to, body, info.Mode().Perm()); err != nil {
			return err
		}
		// The index records each file's modification time; keeping it
		// keeps a copied work tree as clean to git as the template's.
		return os.Chtimes(to, info.ModTime(), info.ModTime())
	})
}
