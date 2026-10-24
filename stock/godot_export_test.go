package stock

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/execution"
)

// The Godot web export check passes only on this run's successful export:
// the import and the export must each exit 0, and index.wasm and index.pck
// must be what this export wrote, not files an earlier one left. Run with a
// stub godot on the host's shell, so it proves the script, not the engine.
//
// The check applies to a part built with godot, so a tree without
// project.godot is the part's failure, said in words, not "not judged". The
// command names its folder as ${PART_FOLDER}, which is replaced with the
// folder as the engine does (the root part's, here the temporary tree).
//
// Kills: the export's exit discarded through a pipe, the import's failure
// suppressed, success decided by files that could predate the run (fallback
// audit F13), and a missing project.godot passed or left unjudged.
func TestTheGodotExportCheckNeedsThisRunsSuccessfulExport(t *testing.T) {
	t.Parallel()
	script := scannerScript(t, "godot-web-export")
	// The stub: GODOT_IMPORT and GODOT_EXPORT are its exit codes; with
	// GODOT_WRITES set, the export writes the two outputs first.
	stub := `#!/bin/sh
case "$*" in
*--import*) exit "${GODOT_IMPORT:-0}" ;;
*--export-release*)
	if [ -n "$GODOT_WRITES" ]; then printf 'wasm' > "$WORK/build/web/index.wasm"; printf 'pck' > "$WORK/build/web/index.pck"; fi
	exit "${GODOT_EXPORT:-0}" ;;
esac
exit 3
`
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "godot"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name      string
		project   bool // project.godot in the tree
		earlier   bool // outputs an earlier export left
		env       []string
		want      int
		wantWords string
	}{
		{"this run's export", true, false, []string{"GODOT_WRITES=1"}, 0, "web export ok"},
		{"the import fails", true, false, []string{"GODOT_IMPORT=2", "GODOT_WRITES=1"}, 1, "the import failed"},
		{"the export writes and fails", true, false, []string{"GODOT_EXPORT=1", "GODOT_WRITES=1"}, 1, "the web export failed"},
		{"an earlier export's files", true, true, nil, 1, "produced no index.wasm"},
		{"no Godot project", false, false, []string{"GODOT_WRITES=1"}, 1, "part game is built with godot (tools: in .apptown/app.yaml) and there is no project.godot in the repository root"},
	} {
		work := t.TempDir()
		if c.project {
			if err := os.WriteFile(filepath.Join(work, "project.godot"), []byte("config_version=5\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if c.earlier {
			if err := os.MkdirAll(filepath.Join(work, "build", "web"), 0o755); err != nil {
				t.Fatal(err)
			}
			for _, f := range []string{"index.wasm", "index.pck"} {
				if err := os.WriteFile(filepath.Join(work, "build", "web", f), []byte("old"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
		}
		out := (execution.Runner{}).Run(t.Context(), execution.Local, execution.Request{Workspace: work,
			Argv: []string{"sh", "-c", strings.ReplaceAll(script, check.PartFolderVar, work)}, Timeout: time.Minute,
			Env: append([]string{"PATH=" + bin + ":" + os.Getenv("PATH"), "WORK=" + work, "APPTOWN_FOLDER=.", "APPTOWN_PARTS_HERE=game"}, c.env...)})
		if !out.Answered() {
			t.Fatalf("%s: the script did not run: %v", c.name, out.RunErr)
		}
		if out.ExitCode != c.want || !strings.Contains(string(out.Output), c.wantWords) {
			t.Errorf("%s: exit %d, want %d saying %q:\n%s", c.name, out.ExitCode, c.want, c.wantWords, out.Output)
		}
	}
}

// scannerScript is the shell script a shipped scanner runs (its command is
// sh -c <script>).
func scannerScript(t *testing.T, name string) string {
	t.Helper()
	raw, err := FS.ReadFile("scanners.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Scanners []struct {
			Name    string   `yaml:"name"`
			Command []string `yaml:"command"`
		} `yaml:"scanners"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for _, s := range doc.Scanners {
		if s.Name == name {
			if len(s.Command) < 3 || s.Command[0] != "sh" || s.Command[1] != "-c" {
				t.Fatalf("%s does not run a shell script: %q", name, s.Command)
			}
			return s.Command[2]
		}
	}
	t.Fatalf("no scanner %s in scanners.yaml", name)
	return ""
}
