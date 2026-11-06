package workflowexec

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/execution"

	"github.com/flokkit/apptown/internal/apptown/agentenv"
)

func TestCleanTreeBuildUsesSelectedWorkspaceAndBoundedExecutor(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The tree's ambient environment travels with the request, so the build
	// finds the Go the tree declares through its workspace's shims.
	//
	// Kills: a clean build without the ambient environment, which built with
	// whatever Go the image had, or with none.
	amb := agentenv.AmbientFor(nil, dir)
	// The check executor's run, which answers from the outcome alone: an
	// exit 1 carries no process error.
	answer := execution.Outcome{Termination: execution.Exited, ExitCode: 1, Output: []byte(strings.Repeat("x", 900))}
	checks := CompileCheck{Image: "agent:image", Run: func(_ context.Context, req execution.Request) execution.Outcome {
		if req.Workspace != dir || req.Image != "agent:image" || req.Timeout != 5*time.Minute || !strings.HasPrefix(req.Name, "apptown-cleanbuild-") || len(req.Mounts) != 0 {
			t.Fatalf("wrong clean build request: %+v", req)
		}
		if !slices.Equal(req.Env, amb.Env) || len(req.Files) != len(amb.Files) || len(amb.Files) == 0 {
			t.Fatalf("the clean build is not given the tree's ambient environment: env %v, %d files (want %v, %d)", req.Env, len(req.Files), amb.Env, len(amb.Files))
		}
		return answer
	}}
	reason, ok, _ := cleanTreeBuilds(t.Context(), dir, checks, amb)
	if ok || !strings.Contains(reason, "go build ./... failed") || len(reason) > 900 {
		t.Fatalf("lost bounded build failure: %v %q", ok, reason)
	}
	for _, out := range []execution.Outcome{{Termination: execution.TimedOut, ExitCode: -1}, {Termination: execution.LaunchFailed, ExitCode: -1}, {}} {
		answer = out
		if _, ok, _ := cleanTreeBuilds(t.Context(), dir, checks, amb); ok {
			t.Fatalf("a build that did not exit 0 passed: %+v", out)
		}
	}
	answer = execution.Outcome{Termination: execution.Exited}
	if _, ok, _ := cleanTreeBuilds(t.Context(), dir, checks, amb); !ok {
		t.Fatal("a clean build failed")
	}
}

// The clean build's script, run here: a Go tree builds with the go its
// declared toolchain puts in the workspace's shims; with no go at all it
// says the project declares none; and a toolchain that did not install says
// so, before anything about go. Each is a rework with those words, not a
// compiler error.
//
// Kills: a tree with no declared Go read as a compiler failure; an install
// failure hidden behind "go: not found"; shims not put on PATH after install.
func TestACleanBuildSaysWhenTheTreeDeclaresNoGo(t *testing.T) {
	t.Parallel()
	// mise and the shim are the system's true and false: the script's
	// handling of their exits and of PATH is what is tested, not theirs.
	for _, tc := range []struct {
		name, mise string
		goShim     bool
		ok         bool
		reason     string
	}{
		{"declared and installed", "/usr/bin/true", true, true, ""},
		{"no Go declared", "/usr/bin/true", false, false, cleanBuildNoGo},
		{"install failed", "/usr/bin/false", true, false, cleanBuildNoInstall},
		{"install failed, no Go", "/usr/bin/false", false, false, cleanBuildNoInstall},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tree, bin, data := t.TempDir(), t.TempDir(), t.TempDir()
			if err := os.WriteFile(filepath.Join(tree, "go.mod"), []byte("module example"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(tc.mise, filepath.Join(bin, "mise")); err != nil {
				t.Fatal(err)
			}
			if tc.goShim {
				if err := os.MkdirAll(filepath.Join(data, "shims"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("/usr/bin/true", filepath.Join(data, "shims", "go")); err != nil {
					t.Fatal(err)
				}
			}
			// PATH is bin alone: the script uses only shell builtins and
			// mise, and a go anywhere on the host's PATH (a CI runner's
			// /usr/bin, say) would answer "no Go declared" wrongly.
			amb := agentenv.Ambient{Env: []string{"PATH=" + bin, "MISE_DATA_DIR=" + data}}
			checks := CompileCheck{Image: "agent:image", Run: func(ctx context.Context, req execution.Request) execution.Outcome {
				return (execution.Runner{}).Run(ctx, execution.Local, execution.Request{Workspace: req.Workspace, Argv: req.Argv, Env: req.Env, Timeout: time.Minute})
			}}
			reason, ok, stopped := cleanTreeBuilds(t.Context(), tree, checks, amb)
			if ok != tc.ok || stopped || reason != tc.reason {
				t.Fatalf("ok %v stopped %v reason %q, want ok %v reason %q", ok, stopped, reason, tc.ok, tc.reason)
			}
		})
	}
}

// A clean build its caller stopped answers nothing: never "does not build",
// which would send the change back for rework.
//
// Kills: a stopped build read as a failed one.
func TestAStoppedCleanBuildIsNotAFailedOne(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example"), 0o644); err != nil {
		t.Fatal(err)
	}
	stopped, cancel := context.WithCancel(t.Context())
	checks := CompileCheck{Image: "agent:image", Run: func(context.Context, execution.Request) execution.Outcome {
		cancel() // the step stops while the tree builds
		return execution.Outcome{Termination: execution.Canceled, ExitCode: -1}
	}}
	if reason, ok, wasStopped := cleanTreeBuilds(stopped, dir, checks, agentenv.Ambient{}); ok || !wasStopped || reason != "" {
		t.Fatalf("a stopped build: ok %v stopped %v %q, want stopped", ok, wasStopped, reason)
	}
}
