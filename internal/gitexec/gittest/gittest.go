// Package gittest runs git for tests through gitexec, so a test's git
// behaves like the forge's own (no optional locks, the folder in PWD) and a
// failure stops the test with git's own words.
package gittest

import (
	"context"
	"os/exec"
	"testing"

	"flokkit.io/forge/internal/gitexec"
)

// Tester is the identity Run gives commits a test makes when the test says
// nothing else, so a test does not depend on the machine's git config.
var Tester = []string{"-c", "user.name=test", "-c", "user.email=test@example.com"}

// Run runs git in dir and returns its trimmed output, failing the test on
// any error. An empty dir is refused (unless git is told --git-dir): git
// would otherwise run in the test's own working directory, which is the
// repository under test, and a stray commit would land there.
func Run(t testing.TB, dir string, args ...string) string {
	t.Helper()
	if dir == "" && (len(args) == 0 || args[0] != "--git-dir") {
		t.Fatal("gittest.Run needs a directory")
	}
	out, err := gitexec.Run(context.Background(), dir, args...)
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return out
}

// RunAsTester is Run with the Tester identity, for tests whose repositories
// configure none.
func RunAsTester(t testing.TB, dir string, args ...string) string {
	t.Helper()
	return Run(t, dir, append(append([]string{}, Tester...), args...)...)
}

// Command is git with args, started by gitexec.Command from
// gitexec.Environ: no GIT_* variable the test inherited reaches it. A test
// run from a Git hook carries the hook's GIT_DIR, GIT_INDEX_FILE and others,
// and git started with them writes to that repository whatever folder it
// runs in. A test that starts git itself uses this (or Run), sets the
// command's Dir, and appends any variables it adds to the command's Env.
func Command(args ...string) *exec.Cmd {
	return CommandContext(context.Background(), args...)
}

// CommandContext is Command with a context.
func CommandContext(ctx context.Context, args ...string) *exec.Cmd {
	return gitexec.Command(ctx, "", gitexec.Environ(), args...)
}
