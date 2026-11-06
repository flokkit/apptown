package workflowexec

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/execution"

	"github.com/flokkit/apptown/internal/apptown/agentenv"
	"github.com/flokkit/apptown/internal/trust"
)

// cleanTreeBuilds compiles the already-selected committed tree in the agent
// image, where checks run (a worker's executor when one is configured), with
// the executor's own caches and amb, the tree's ambient build environment:
// the Go it builds with is the one its mise.toml declares, as in its tests
// step (the agent image has no Go of its own). Go-only for now (go.mod
// present); other toolchains vacuously pass. Returns (reason, ok).
// A build its caller stopped answers nothing (stopped): it is never read as
// a tree that does not build, which would send the change back for rework.
func cleanTreeBuilds(ctx context.Context, staged string, checks CompileCheck, amb agentenv.Ambient) (reason string, ok, stopped bool) {
	if _, err := os.Stat(filepath.Join(staged, "go.mod")); err != nil {
		return "", true, false // not a Go project — nothing to check yet
	}
	outcome := checks.Run(ctx, execution.Request{
		Workspace: staged,
		Argv: []string{"sh", "-c",
			trust.Run + "; mise trust -q 2>/dev/null; mise install -y -q 2>&1 || { echo '" + cleanBuildNoInstall + "'; exit 1; }; " +
				`export PATH="$MISE_DATA_DIR/shims:$PATH"; ` +
				"command -v go >/dev/null || { echo '" + cleanBuildNoGo + "'; exit 1; }; go build ./... 2>&1"},
		Env:     amb.Env,
		Files:   amb.Files,
		Image:   checks.Image,
		Timeout: 5 * time.Minute,
		Name:    execution.UniqueName("apptown-cleanbuild"),
	})
	if !outcome.Exited0() && ctx.Err() != nil {
		return "", false, true
	}
	if !outcome.Exited0() {
		out := string(outcome.Output)
		for _, mark := range []string{cleanBuildNoGo, cleanBuildNoInstall} {
			if strings.Contains(out, mark) {
				return mark, false, false
			}
		}
		tail := out
		if len(tail) > 800 {
			tail = tail[len(tail)-800:]
		}
		return "go build ./... failed on the committed tree:\n" + tail, false, false
	}
	return "", true, false
}

// What the clean build says when the tree has no Go to build with, and when
// the toolchain it declares did not install, in place of a compiler error.
const (
	cleanBuildNoGo      = "this is a Go project (go.mod) and it declares no Go toolchain: pin one in mise.toml, for example go = \"1.27.0\""
	cleanBuildNoInstall = "the toolchain this project declares in mise.toml did not install, so the committed tree was not built"
)
