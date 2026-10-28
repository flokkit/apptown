//go:build unix

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/execfile"

	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/poll"
)

// interruptChild names the folder of the child run of
// TestAnInterruptedCheckLeavesNoGitBehind.
const interruptChild = "APPTOWN_TEST_INTERRUPT_CHILD"

// Ctrl-C during a check's push stops the push as well as the CLI. The
// terminal interrupts its foreground process group; gitexec puts git in a
// group of its own, so only the CLI hears it. The CLI runs here as a child
// in a group of its own (execution.Runner gives it one), with a git on PATH
// that starts a long-lived child and records its pid; once git has
// started, the child interrupts its own group, as the terminal would. The
// CLI ends as interrupted, and git's process group (git and its child) is
// gone.
func TestAnInterruptedCheckLeavesNoGitBehind(t *testing.T) {
	if dir := os.Getenv(interruptChild); dir != "" {
		go func() { _ = sendScratch("0123456789012345678901234567890123456789", "refs/apptown/scratch/1") }()
		if !poll.Until(context.Background(), 10*time.Second, 10*time.Millisecond, func() bool {
			// Redirection creates the file before echo writes the PID.
			// Existence alone is not the process witness this test needs.
			raw, err := os.ReadFile(filepath.Join(dir, "started"))
			pid, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
			return err == nil && pid > 0
		}) {
			t.Fatal("git never wrote its PID")
		}
		// git leads a group of its own, so the parent's witness of its
		// end says something.
		raw, _ := os.ReadFile(filepath.Join(dir, "started"))
		if pid, _ := strconv.Atoi(strings.TrimSpace(string(raw))); pid <= 0 || syscall.Kill(-pid, 0) != nil {
			t.Fatalf("git (pid %q) does not lead a process group of its own", raw)
		}
		if err := syscall.Kill(0, syscall.SIGINT); err != nil {
			t.Fatal(err)
		}
		// The interrupted CLI exits from under this wait, which only bounds
		// how long it may take.
		_ = poll.Wait(t.Context(), 10*time.Second)
		t.Fatal("the interrupted CLI kept running")
	}
	dir := t.TempDir()
	// git leads a group of its own (gitexec): it starts a child that would
	// outlive any test, then writes its pid, the group's id.
	git := "#!/bin/sh\nsleep 300 &\necho $$ > '" + filepath.Join(dir, "started") + "'\nwait\n"
	if err := execfile.Write(filepath.Join(dir, "git"), []byte(git), 0o755); err != nil {
		t.Fatal(err)
	}
	out := (execution.Runner{}).Run(context.Background(), execution.Local, execution.Request{
		Argv: []string{os.Args[0], "-test.run=^TestAnInterruptedCheckLeavesNoGitBehind$"},
		Env: []string{interruptChild + "=" + dir, "PATH=" + dir + string(os.PathListSeparator) + os.Getenv("PATH"),
			"APPTOWN_URL=http://127.0.0.1:1", "APPTOWN_PROJECT=p", "APPTOWN_SESSION_TOKEN=t"},
		Timeout: 30 * time.Second,
	})
	if out.Termination != execution.Exited || out.ExitCode != 128+int(syscall.SIGINT) {
		t.Fatalf("the CLI did not end as interrupted: %s %d %v\n%s", out.Termination, out.ExitCode, out.RunErr, out.Output)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "started"))
	pgid, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pgid <= 0 {
		t.Fatalf("git's pid: %q %v", raw, err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-pgid, syscall.SIGKILL) })
	// Every process the push started is gone: git's group no longer exists,
	// once init has reaped what was killed.
	if !poll.Until(t.Context(), 5*time.Second, 10*time.Millisecond, func() bool {
		return errors.Is(syscall.Kill(-pgid, 0), syscall.ESRCH)
	}) {
		t.Fatal("git carried on after the CLI was interrupted")
	}
}
