package stock

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/execution"
)

// npm-build and npm-test run where the app's node packages are: the part's
// own folder when it has a package.json, otherwise each top-level package
// below it (a Go app with its front end in frontend/), never one inside
// another or in node_modules. A part with none says so, naming "or below it".
// Run with a stub npm on the host's shell, so it proves the script.
//
// Kills: the root-only rule that reported nextup's frontend/ app as "no
// package.json in the repository root" (3 Nov).
func TestNPMChecksFindThePackagesBelowThePart(t *testing.T) {
	t.Parallel()
	bin := t.TempDir()
	stub := `#!/bin/sh
case "$*" in
ci*) exit 0 ;;
"pkg get scripts.build"|"pkg get scripts.test") echo '"x"' ;;
"run build"|test) pwd >> "$WORK/ran" ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "npm"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name  string
		files []string
		want  int
		ran   []string
		words string
	}{
		{"the part's own package", []string{"package.json", "package-lock.json", "web/package.json", "web/package-lock.json"}, 0, []string{"."}, ""},
		{"a front end below a Go app", []string{"go.mod", "frontend/package.json", "frontend/package-lock.json", "frontend/node_modules/x/package.json", "frontend/pkg/inner/package.json", "tools/site/package.json", "tools/site/package-lock.json"}, 0, []string{"frontend", "tools/site"}, ""},
		{"no package anywhere", []string{"go.mod"}, 1, nil, "no package.json in the repository root or below it"},
		{"a package without its lockfile", []string{"frontend/package.json"}, 1, nil, "frontend: no package-lock.json"},
	} {
		for _, scanner := range []string{"npm-build", "npm-test"} {
			work := t.TempDir()
			for _, f := range c.files {
				if err := os.MkdirAll(filepath.Join(work, filepath.Dir(f)), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(work, f), []byte("{}"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			out := (execution.Runner{}).Run(t.Context(), execution.Local, execution.Request{Workspace: work,
				Argv: []string{"sh", "-c", strings.ReplaceAll(scannerScript(t, scanner), check.PartFolderVar, work)}, Timeout: time.Minute,
				Env: []string{"PATH=" + bin + ":" + os.Getenv("PATH"), "WORK=" + work, "APPTOWN_FOLDER=.", "APPTOWN_PARTS_HERE=web"}})
			if !out.Answered() {
				t.Fatalf("%s %s: the script did not run: %v", scanner, c.name, out.RunErr)
			}
			if out.ExitCode != c.want || !strings.Contains(string(out.Output), c.words) {
				t.Errorf("%s %s: exit %d, want %d saying %q:\n%s", scanner, c.name, out.ExitCode, c.want, c.words, out.Output)
			}
			raw, _ := os.ReadFile(filepath.Join(work, "ran"))
			var ran []string
			for _, l := range strings.Fields(string(raw)) {
				rel, _ := filepath.Rel(work, l)
				ran = append(ran, rel)
			}
			if strings.Join(ran, ",") != strings.Join(c.ran, ",") {
				t.Errorf("%s %s: ran in %v, want %v", scanner, c.name, ran, c.ran)
			}
		}
	}
}
