package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/execution"
)

// Every worker flag is also its APPTOWN_ variable, and the command line
// wins: the worker started with APPTOWN_CERT naming a missing file says so,
// and with -cert naming another, names that one instead.
func TestTheWorkersFlagsAreAlsoItsVariables(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	binary := filepath.Join(dir, "apptown-worker")
	if build := (execution.Runner{}).Run(context.Background(), execution.Local, execution.Request{
		Argv: []string{"go", "build", "-p", "2", "-o", binary, "github.com/flokkit/apptown/cmd/apptown-worker"}, Timeout: 10 * time.Minute,
	}); build.RunErr != nil {
		t.Fatalf("build: %v %s", build.RunErr, build.Output)
	}
	start := func(args ...string) string {
		out := (execution.Runner{}).Run(context.Background(), execution.Local, execution.Request{
			Workspace: dir, Argv: append([]string{binary}, args...), Timeout: time.Minute,
			Env: []string{"APPTOWN_CERT=" + filepath.Join(dir, "from-variable.pem"), "APPTOWN_STATE=" + filepath.Join(dir, "state"),
				"APPTOWN_INSTALLATION=forge.example", "APPTOWN_KEY=" + filepath.Join(dir, "key.pem"), "APPTOWN_CLIENT_CA=" + filepath.Join(dir, "ca.pem")},
		})
		return string(out.Output)
	}
	if out := start(); !strings.Contains(out, "from-variable.pem") {
		t.Fatalf("the variable was not read: %s", out)
	}
	if out := start("-cert", filepath.Join(dir, "from-flag.pem")); !strings.Contains(out, "from-flag.pem") || strings.Contains(out, "from-variable.pem") {
		t.Fatalf("the command line did not win: %s", out)
	}
}
