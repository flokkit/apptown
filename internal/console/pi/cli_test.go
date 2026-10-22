package pi

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/execfile"
)

// CLI runs the binary it names with the arguments given and answers its
// stdout and stderr together, with the command's failure.
func TestCLIRunsTheNamedBinary(t *testing.T) {
	t.Parallel()
	binary := filepath.Join(t.TempDir(), "chosen-docker")
	if err := execfile.Write(binary, []byte("#!/bin/sh\necho \"out $*\"\necho err >&2\nexit 4\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := CLI(binary)(context.Background(), "ps", "-q")
	if err == nil || !strings.Contains(string(out), "out ps -q") || !strings.Contains(string(out), "err") {
		t.Fatalf("%q %v", out, err)
	}
}
