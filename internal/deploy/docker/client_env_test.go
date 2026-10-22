package docker

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/execfile"
)

// A secret given to a container by name (`-e PGPASSWORD`) reaches the
// docker CLI through its environment, never its arguments, which any
// process on the host can read.
func TestACommandsEnvironmentReachesTheCLIOutsideItsArguments(t *testing.T) {
	t.Parallel()
	bin := filepath.Join(t.TempDir(), "docker")
	if err := execfile.Write(bin, []byte("#!/bin/sh\necho \"args=$* env=$PGPASSWORD\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := Scope{Client: CLI{Binary: bin}}.Command(context.Background(), Command{Args: []string{"run", "-e", "PGPASSWORD", "img"}, Env: []string{"PGPASSWORD=s3cret"}})
	if err != nil || strings.TrimSpace(string(out)) != "args=run -e PGPASSWORD img env=s3cret" {
		t.Fatalf("%q %v", out, err)
	}
}
