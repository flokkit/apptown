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
	out, err := Scope{Client: CLI{Binary: echoingDocker(t, "PGPASSWORD")}}.Command(context.Background(), Command{Args: []string{"run", "-e", "PGPASSWORD", "img"}, Env: []string{"PGPASSWORD=s3cret"}})
	if err != nil || strings.TrimSpace(string(out)) != "args=run -e PGPASSWORD img env=s3cret" {
		t.Fatalf("%q %v", out, err)
	}
}

// echoingDocker is a docker binary that prints its arguments and the
// variable name from its environment: what the CLI it stands for receives.
func echoingDocker(t *testing.T, name string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "docker")
	if err := execfile.Write(bin, []byte("#!/bin/sh\nprintf 'args=%s env=%s\\n' \"$*\" \"$"+name+"\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}
