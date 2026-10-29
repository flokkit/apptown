// Package testinputs makes the repository files a test hands to a child
// process inputs of that test's cached result.
//
// Go's test cache reuses a package's passing result while the test binary,
// its flags, the environment variables it read and the module files it
// opened or stat-ed are unchanged. A file that only a child process reads —
// a script run with sh or node, a docker build context, the sources of a
// binary built with go build — is not among them, so editing it alone would
// replay a stale pass. A test calls Files or GoBuild before starting that
// process: reading the files in the test process records them.
package testinputs

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/execution"
)

// Files reads every regular file under each path (a file or a directory,
// relative to the test's working directory), and lists every directory, so a
// changed, added or removed file changes the test's cache key.
func Files(t testing.TB, paths ...string) {
	t.Helper()
	for _, root := range paths {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			switch {
			case d.IsDir():
				_, err = os.ReadDir(path)
			case d.Type().IsRegular():
				_, err = os.ReadFile(path)
			}
			return err
		})
		if err != nil {
			t.Fatalf("reading the test's inputs under %s: %v", root, err)
		}
	}
}

// listTemplate prints, for each package of this module that the named
// packages need, its directory, its Go and embedded files, and the module's
// go.mod; a dependency outside the module is fixed by go.sum, read beside
// go.mod. The directory is listed, not only its files, so a file added to a
// package changes the inputs too. Each line carries a prefix: the runner
// returns go's stderr in the same output, and only prefixed lines name files.
const listTemplate = `{{if and .Module .Module.Main}}{{$dir := .Dir}}` + prefix + `{{.Module.GoMod}}
` + prefix + `{{$dir}}
{{range .GoFiles}}` + prefix + `{{$dir}}/{{.}}
{{end}}{{range .CgoFiles}}` + prefix + `{{$dir}}/{{.}}
{{end}}{{range .EmbedFiles}}` + prefix + `{{$dir}}/{{.}}
{{end}}{{end}}`

const prefix = "testinput "

// GoBuild reads the sources of everything `go build pkgs...` run in dir
// compiles from this module (go list -deps, with the build's GOOS), so a
// change to a binary the test builds and runs in a child process changes the
// test's cache key, as a change to the test's own packages does. It returns
// the files it read.
func GoBuild(t testing.TB, dir string, env []string, pkgs ...string) []string {
	t.Helper()
	out := (execution.Runner{}).Run(t.Context(), execution.Local, execution.Request{
		Workspace: dir, Env: env, Timeout: 2 * time.Minute,
		Argv: append([]string{"go", "list", "-deps", "-f", listTemplate}, pkgs...),
	})
	if !out.Exited0() {
		t.Fatalf("listing the sources of %v: %v\n%s", pkgs, out.RunErr, out.Output)
	}
	seen := map[string]bool{}
	var read []string
	for _, line := range strings.Split(string(out.Output), "\n") {
		line, ok := strings.CutPrefix(line, prefix)
		if !ok || seen[line] {
			continue
		}
		seen[line] = true
		files := []string{line}
		if filepath.Base(line) == "go.mod" {
			files = append(files, filepath.Join(filepath.Dir(line), "go.sum"))
		}
		for _, f := range files {
			if err := readInput(f); err != nil {
				t.Fatalf("reading %s, a source of %v: %v", f, pkgs, err)
			}
			read = append(read, f)
		}
		// An embedded file's directory, for a file added under an embed pattern.
		if dir := filepath.Dir(line); !seen[dir] && !strings.HasSuffix(line, ".go") && filepath.Base(line) != "go.mod" {
			seen[dir] = true
			if err := readInput(dir); err != nil {
				t.Fatalf("listing %s, a source of %v: %v", dir, pkgs, err)
			}
			read = append(read, dir)
		}
	}
	if len(seen) == 0 {
		t.Fatalf("go list named no sources of %v in this module", pkgs)
	}
	return read
}

// readInput reads a file, or lists a directory, in this process.
func readInput(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		_, err = os.ReadDir(path)
	} else {
		_, err = os.ReadFile(path)
	}
	return err
}
