package testinputs

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// GoBuild must name what the build compiles from this module, including a
// main package the test binary does not link and the files it embeds, and
// nothing from the standard library or the module cache, whose content is
// fixed by the toolchain and go.sum.
func TestGoBuildReadsTheModuleSourcesABinaryIsBuiltFrom(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	read := GoBuild(t, root, []string{"GOOS=linux", "CGO_ENABLED=0"}, "./cmd/apptown")
	for _, want := range []string{
		filepath.Join(root, "go.mod"),
		filepath.Join(root, "go.sum"),
		filepath.Join(root, "cmd", "apptown", "main.go"),
	} {
		if !slices.Contains(read, want) {
			t.Errorf("did not read %s", want)
		}
	}
	var embedded, library bool
	for _, f := range read {
		if !strings.HasPrefix(f, root+string(filepath.Separator)) {
			t.Errorf("read %s, outside the module", f)
		}
		embedded = embedded || strings.HasPrefix(f, filepath.Join(root, "internal", "trust")+string(filepath.Separator)) && !strings.HasSuffix(f, ".go")
		library = library || strings.HasPrefix(f, filepath.Join(root, "internal", "execution")+string(filepath.Separator))
	}
	if !embedded {
		t.Error("read none of internal/trust's embedded files, which the CLI is built with")
	}
	if !library {
		t.Error("read none of internal/execution, a package the CLI imports")
	}
}

func TestFilesRefusesAPathThatIsNotThere(t *testing.T) {
	ft := &fatalRecorder{TB: t}
	func() {
		defer func() { _ = recover() }()
		Files(ft, filepath.Join(t.TempDir(), "missing"))
	}()
	if !ft.failed {
		t.Fatal("a missing input was not a failure")
	}
}

type fatalRecorder struct {
	testing.TB
	failed bool
}

func (f *fatalRecorder) Helper() {}

func (f *fatalRecorder) Fatalf(string, ...any) {
	f.failed = true
	panic("fatal")
}

// Every test that builds a binary with go build in a child process names
// that build's sources with GoBuild, unless the binary is the package under
// test (its sources are in the test binary) or the test is never cached
// (scripts/ci-uncached-tests.txt).
func TestEveryChildGoBuildNamesItsSources(t *testing.T) {
	exempt := map[string]string{
		"cmd/apptown-worker/env_flags_test.go":   "builds the package under test",
		"cmd/apptown-worker/docker_flag_test.go": "builds the package under test",
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "private" || d.Name() == "node_modules" || strings.HasPrefix(d.Name(), ".")) && path != root {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !strings.Contains(string(src), `"go", "build"`) {
			return nil
		}
		found++
		rel, _ := filepath.Rel(root, path)
		if _, ok := exempt[rel]; !ok && !strings.Contains(string(src), "testinputs.GoBuild(") {
			t.Errorf("%s builds with go build in a child process without testinputs.GoBuild", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if found < len(exempt) {
		t.Fatalf("found %d child go builds; the scan is not seeing the tree", found)
	}
}
