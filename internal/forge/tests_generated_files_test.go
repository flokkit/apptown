package forge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flokkit.io/forge/internal/execfile"

	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/workflow"
)

// A failure to stage generated configuration must settle as an error, not
// become a carried test failure when neither candidate nor base ran.
func TestTestsThatCannotStartAreAnErrorNeverCarried(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	scratch := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(scratch, []byte("sentinel"), 0600); err != nil {
		t.Fatal(err)
	}
	calls := filepath.Join(dir, "docker-calls")
	binary := filepath.Join(dir, "docker")
	if err := execfile.Write(binary, []byte("#!/bin/sh\necho called >> '"+calls+"'\nexit 1\n"), 0755); err != nil {
		t.Fatal(err)
	}
	a := testApp(t, Config{ProcessRunner: execution.Runner{DockerBinary: binary, Scratch: scratch}})
	bindNodeMirror(t, a, "https://mirror.invalid/node/")
	run, _ := reviewLanding(t, a, []schema.Step{{Name: "tests", Uses: "tests", With: map[string]any{"command": "exit 1"}}}, map[string]string{"value.txt": "candidate\n"}, nil)
	step := reviewExecute(t, a, run, "tests")
	if b, err := os.ReadFile(calls); !os.IsNotExist(err) {
		t.Fatalf("Docker was called: %s (%v)", b, err)
	}
	if step.Status != workflow.StatusError {
		t.Fatalf("generated-file staging failed before any Docker call; got %s: %s", step.Status, step.Evidence)
	}
}

// Both attempts receive the same admitted bytes at the same read-only path,
// with each mount source outside its project tree.
func TestTheBaseRerunGetsTheSameGeneratedFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	calls := filepath.Join(dir, "docker-calls")
	binary := filepath.Join(dir, "docker")
	script := `#!/bin/sh
case "$1" in inspect) echo false; exit 0;; rm) exit 0;; esac
prev=""
for arg in "$@"; do
 if [ "$prev" = "-v" ]; then
  case "$arg" in
   *:/work) workspace="${arg%:/work}";;
   *:/etc/mise/config.toml:ro) config="${arg%:/etc/mise/config.toml:ro}";;
  esac
 fi
 prev="$arg"
done
[ -n "$config" ] || exit 2
case "$config" in "$workspace"/*) exit 3;; esac
cat "$config" >> '` + calls + `'
echo END >> '` + calls + `'
exit 1
`
	if err := execfile.Write(binary, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	a := testApp(t, Config{ProcessRunner: execution.Runner{DockerBinary: binary}})
	bindNodeMirror(t, a, "https://mirror.invalid/node/")
	run, _ := reviewLanding(t, a, []schema.Step{{Name: "tests", Uses: "tests", With: map[string]any{"command": "exit 1"}}}, map[string]string{"value.txt": "candidate\n"}, nil)
	step := reviewExecute(t, a, run, "tests")
	b, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(string(b), "END\n")
	if len(parts) != 3 || parts[0] != parts[1] || !strings.Contains(parts[0], "https://mirror.invalid/node/") {
		t.Fatalf("candidate/base configuration differs or is absent: %q", b)
	}
	if step.Status != workflow.StatusPass || !strings.Contains(string(step.Evidence), "carried") {
		t.Fatalf("ordinary nonzero exits: %s %s", step.Status, step.Evidence)
	}
}

// A candidate witnessed failing is carried only by a base witnessed failing:
// a base that could not start says nothing about the base, and the change's
// failure stands.
func TestABaseThatCannotStartNeverCarriesAFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	binary := filepath.Join(dir, "docker")
	// The candidate runs and fails; once its container is removed the
	// Docker it used is gone, so the base cannot start.
	if err := execfile.Write(binary, []byte("#!/bin/sh\ncase \"$1\" in\ninspect) echo false; exit 0;;\nrm) chmod -x \"$0\"; exit 0;;\nesac\nexit 1\n"), 0755); err != nil {
		t.Fatal(err)
	}
	a := testApp(t, Config{ProcessRunner: execution.Runner{DockerBinary: binary}})
	bindNodeMirror(t, a, "https://mirror.invalid/node/")
	run, _ := reviewLanding(t, a, []schema.Step{{Name: "tests", Uses: "tests", With: map[string]any{"command": "exit 1"}}}, map[string]string{"value.txt": "candidate\n"}, nil)
	step := reviewExecute(t, a, run, "tests")
	if step.Status != workflow.StatusFail || strings.Contains(string(step.Evidence), "carried") {
		t.Fatalf("a failure carried by a base that never ran: %s %s", step.Status, step.Evidence)
	}
}
