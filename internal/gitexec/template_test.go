package gitexec

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// repoTemplate is a repository a test helper builds once for the package
// and copies for each test, in Go, so a test's own repository starts no
// git. It is gittest.Template in small: gittest imports this package, so
// its tests cannot import gittest.
type repoTemplate struct {
	build func(t testing.TB, dir string)
	once  sync.Once
	dir   string
	built bool
}

var templateRoot struct {
	sync.Mutex
	dir string
}

func TestMain(m *testing.M) {
	code := m.Run()
	templateRoot.Lock()
	if templateRoot.dir != "" {
		_ = os.RemoveAll(templateRoot.dir)
	}
	templateRoot.Unlock()
	os.Exit(code)
}

// copyTo makes dst, which must not exist, the test's own copy.
func (tp *repoTemplate) copyTo(t *testing.T, dst string) {
	t.Helper()
	tp.once.Do(func() {
		templateRoot.Lock()
		if templateRoot.dir == "" {
			dir, err := os.MkdirTemp("", "gitexec-templates-")
			if err != nil {
				templateRoot.Unlock()
				t.Fatal(err)
			}
			templateRoot.dir = dir
		}
		dir, err := os.MkdirTemp(templateRoot.dir, "")
		templateRoot.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		tp.dir = dir
		tp.build(t, dir)
		tp.built = true
	})
	if !tp.built {
		t.Fatal("the template repository did not build (see the first test that asked for it)")
	}
	err := filepath.Walk(tp.dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(tp.dir, path)
		if err != nil {
			return err
		}
		to := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(to, info.Mode().Perm()|0o700)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.WriteFile(to, body, info.Mode().Perm()); err != nil {
			return err
		}
		return os.Chtimes(to, info.ModTime(), info.ModTime())
	})
	if err != nil {
		t.Fatalf("copy the template: %v", err)
	}
}
