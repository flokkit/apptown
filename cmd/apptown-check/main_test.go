package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/execfile"

	"github.com/flokkit/apptown/internal/execution"
)

// The standalone run says what the gate would say: a tree that breaks a check
// does not conform and the command exits 1; a tree that keeps it exits 0. The
// scripts run in this test's own shell rather than a container, which is the
// only seam — everything else is the library the gate uses.
func TestAppTownCheckReportsAndExits(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	checks := filepath.Join(dir, "checks.yaml")
	if err := os.WriteFile(checks, []byte(`checks:
  - name: no-fixme
    expects: nothing is left half-done
    severity: high
    sh: 'if grep -rl FIXME . >/dev/null 2>&1; then echo "FIXME left in the tree"; exit 1; fi'
`), 0o644); err != nil {
		t.Fatal(err)
	}

	tree := t.TempDir()
	if err := os.WriteFile(filepath.Join(tree, "a.txt"), []byte("all good here"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	// The report is dated by the program's clock.
	at := func() time.Time { return time.Date(2031, 1, 2, 3, 4, 5, 0, time.UTC) }
	if code := run(t.Context(), []string{"-tree", tree, "-checks", checks}, &out, os.Stderr, execution.Local, at); code != 0 {
		t.Fatalf("a tree that conforms exits 0, got %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), `"outcome": "conforms"`) || !strings.Contains(out.String(), `"at": "2031-01-02T03:04:05Z"`) {
		t.Fatalf("the run says what it found, and when on its clock: %s", out.String())
	}

	if err := os.WriteFile(filepath.Join(tree, "b.txt"), []byte("// FIXME hurry"), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := run(t.Context(), []string{"-tree", tree, "-checks", checks, "-block", "hygiene", "-version", "2"}, &out, os.Stderr, execution.Local, at); code != 1 {
		t.Fatalf("a tree that does not conform exits 1, got %d: %s", code, out.String())
	}
	var report struct {
		Evaluations []struct {
			Block, Check, Outcome, Output string
		}
		Findings []struct{ Fingerprint, Detail string }
	}
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("the run prints JSON: %v\n%s", err, out.String())
	}
	if len(report.Evaluations) != 1 || report.Evaluations[0].Outcome != "does-not-conform" {
		t.Fatalf("evaluations: %+v", report.Evaluations)
	}
	if len(report.Findings) != 1 || report.Findings[0].Fingerprint != "guidance|hygiene@2|no-fixme|" {
		t.Fatalf("the finding carries the fingerprint an exception would match: %+v", report.Findings)
	}
	if report.Findings[0].Detail != "FIXME left in the tree" {
		t.Fatalf("the check's own words are the detail: %+v", report.Findings)
	}
}

// A tool's command in the image reports the command's own exit status and
// words, and the image's run gets no network. Not parallel: it puts a fake
// docker on PATH.
func TestRunInImageReportsTheCommandsExit(t *testing.T) {
	bin := t.TempDir()
	script := "#!/bin/sh\necho \"$*\"\ncase \"$*\" in *fails) exit 3;; esac\nexit 0\n"
	if err := execfile.Write(filepath.Join(bin, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if code, out := runInImage(t.Context(), "app:1", []string{"fails"}, []string{"A=b"}); code != 3 || !strings.Contains(out, "run --rm --network none -e A=b app:1 fails") {
		t.Fatalf("a failing command: %d %q", code, out)
	}
	if code, out := runInImage(t.Context(), "app:1", []string{"passes"}, nil); code != 0 || !strings.Contains(out, "app:1 passes") {
		t.Fatalf("a passing command: %d %q", code, out)
	}
}
