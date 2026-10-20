package dockertest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flokkit.io/forge/internal/execfile"
)

// fakeDocker puts a docker first on PATH that prints its arguments and
// BUILDX_BUILDER to stdout, a warning to stderr, and fails when asked to.
func fakeDocker(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	script := "#!/bin/sh\necho \"args=$* builder=$BUILDX_BUILDER\"\necho warning >&2\n[ \"$1\" = fail ] && exit 3\nexit 0\n"
	if err := execfile.Write(filepath.Join(bin, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// Output is stdout alone, so ids and formatted answers are not polluted by
// warnings; CombinedOutput has both; a failure says what docker said. The
// docker started is AppTown's: an ambient Buildx builder does not reach
// it. Not parallel: it sets the process's environment.
func TestDockerStartsAsTheForgesWithTheOutputAsked(t *testing.T) {
	fakeDocker(t)
	t.Setenv("BUILDX_BUILDER", "elsewhere")
	out, err := Output("ps", "-q")
	if err != nil || string(out) != "args=ps -q builder=\n" {
		t.Fatalf("Output: %q %v", out, err)
	}
	both, err := CombinedOutput("ps")
	if err != nil || !strings.Contains(string(both), "args=ps") || !strings.Contains(string(both), "warning") {
		t.Fatalf("CombinedOutput: %q %v", both, err)
	}
	if err := Run("ps"); err != nil {
		t.Fatal(err)
	}
	if _, err := Output("fail"); err == nil || !strings.Contains(err.Error(), "warning") {
		t.Fatalf("a failure without docker's words: %v", err)
	}
	if err := Run("fail"); err == nil {
		t.Fatal("Run reported a failure as success")
	}
}
